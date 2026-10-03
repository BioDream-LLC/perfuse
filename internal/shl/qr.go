package shl

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"strings"
)

// A QR code encoder, byte mode, for showing a SMART Health Link at a front desk.
//
// Written here rather than imported because it is small, it is the one piece the feature cannot do without, and a dependency for it
// would be the only one in the binary that draws pictures. It follows ISO/IEC 18004 and the structure of Project Nayuki's reference
// implementation; it is checked by decoding its output with zbar, an independent reader, over a range of lengths and versions.

// ECC is an error correction level.
type ECC int

// Error correction levels. M recovers about 15% of a damaged code, which suits a phone screen held up to a camera.
const (
	ECCLow ECC = iota
	ECCMedium
)

var eccCodewordsPerBlock = [2][41]int{
	{-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28},
}

var numErrorCorrectionBlocks = [2][41]int{
	{-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25},
	{-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49},
}

// formatBits is the level's two-bit code in the format information: L is 01, M is 00.
var formatBitsFor = [2]int{1, 0}

// QR is an encoded code: Size modules square, Dark[y][x].
type QR struct {
	Size    int
	Version int
	dark    [][]bool
	isFunc  [][]bool
}

// Dark reports whether a module is dark.
func (q *QR) Dark(x, y int) bool { return x >= 0 && y >= 0 && x < q.Size && y < q.Size && q.dark[y][x] }

func rawDataModules(ver int) int {
	result := (16*ver+128)*ver + 64
	if ver >= 2 {
		numAlign := ver/7 + 2
		result -= (25*numAlign-10)*numAlign - 55
		if ver >= 7 {
			result -= 36
		}
	}
	return result
}

func dataCodewords(ver int, ecl ECC) int {
	return rawDataModules(ver)/8 - eccCodewordsPerBlock[ecl][ver]*numErrorCorrectionBlocks[ecl][ver]
}

// EncodeQR encodes text in byte mode at the smallest version that fits.
func EncodeQR(text string, ecl ECC) (*QR, error) {
	data := []byte(text)
	ver := 0
	for v := 1; v <= 40; v++ {
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if 4+countBits+8*len(data) <= dataCodewords(v, ecl)*8 {
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, fmt.Errorf("%d bytes is more than a QR code holds", len(data))
	}

	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>i)&1 == 1)
		}
	}
	put(0x4, 4)
	if ver >= 10 {
		put(len(data), 16)
	} else {
		put(len(data), 8)
	}
	for _, b := range data {
		put(int(b), 8)
	}
	capacity := dataCodewords(ver, ecl) * 8
	put(0, min(4, capacity-len(bits)))
	put(0, (8-len(bits)%8)%8)
	for pad := 0xEC; len(bits) < capacity; pad ^= 0xEC ^ 0x11 {
		put(pad, 8)
	}
	codewords := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b {
			codewords[i>>3] |= 1 << (7 - uint(i&7))
		}
	}

	q := &QR{Version: ver, Size: ver*4 + 17}
	q.dark = make([][]bool, q.Size)
	q.isFunc = make([][]bool, q.Size)
	for i := range q.dark {
		q.dark[i] = make([]bool, q.Size)
		q.isFunc[i] = make([]bool, q.Size)
	}
	q.drawFunctionPatterns(ecl)
	q.drawCodewords(q.addECCAndInterleave(codewords, ecl))

	best, bestPenalty := 0, -1
	for mask := 0; mask < 8; mask++ {
		q.applyMask(mask)
		q.drawFormatBits(ecl, mask)
		if p := q.penalty(); bestPenalty < 0 || p < bestPenalty {
			best, bestPenalty = mask, p
		}
		q.applyMask(mask) // XOR undoes it
	}
	q.applyMask(best)
	q.drawFormatBits(ecl, best)
	return q, nil
}

func (q *QR) set(x, y int, dark bool) {
	q.dark[y][x] = dark
	q.isFunc[y][x] = true
}

func (q *QR) drawFunctionPatterns(ecl ECC) {
	for i := 0; i < q.Size; i++ {
		q.set(6, i, i%2 == 0)
		q.set(i, 6, i%2 == 0)
	}
	q.drawFinder(3, 3)
	q.drawFinder(q.Size-4, 3)
	q.drawFinder(3, q.Size-4)

	pos := alignmentPositions(q.Version)
	n := len(pos)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == 0 && j == 0 || i == 0 && j == n-1 || i == n-1 && j == 0 {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					q.set(pos[i]+dx, pos[j]+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	q.drawFormatBits(ecl, 0)
	q.drawVersion()
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (q *QR) drawFinder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || y < 0 || x >= q.Size || y >= q.Size {
				continue
			}
			d := max(abs(dx), abs(dy))
			q.set(x, y, d != 2 && d != 4)
		}
	}
}

func alignmentPositions(ver int) []int {
	if ver == 1 {
		return nil
	}
	numAlign := ver/7 + 2
	step := (ver*8 + numAlign*3 + 5) / (numAlign*4 - 4) * 2
	out := make([]int, numAlign)
	out[0] = 6
	for i, pos := numAlign-1, ver*4+17-7; i >= 1; i, pos = i-1, pos-step {
		out[i] = pos
	}
	return out
}

func (q *QR) drawFormatBits(ecl ECC, mask int) {
	data := formatBitsFor[ecl]<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>i)&1 == 1 }

	for i := 0; i <= 5; i++ {
		q.set(8, i, bit(i))
	}
	q.set(8, 7, bit(6))
	q.set(8, 8, bit(7))
	q.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		q.set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		q.set(q.Size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		q.set(8, q.Size-15+i, bit(i))
	}
	q.set(8, q.Size-8, true)
}

func (q *QR) drawVersion() {
	if q.Version < 7 {
		return
	}
	rem := q.Version
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	bits := q.Version<<12 | rem
	for i := 0; i < 18; i++ {
		b := (bits>>i)&1 == 1
		a, c := q.Size-11+i%3, i/3
		q.set(a, c, b)
		q.set(c, a, b)
	}
}

func (q *QR) addECCAndInterleave(data []byte, ecl ECC) []byte {
	numBlocks := numErrorCorrectionBlocks[ecl][q.Version]
	eccLen := eccCodewordsPerBlock[ecl][q.Version]
	raw := rawDataModules(q.Version) / 8
	numShort := numBlocks - raw%numBlocks
	shortLen := raw / numBlocks

	divisor := rsDivisor(eccLen)
	var blocks [][]byte
	k := 0
	for i := 0; i < numBlocks; i++ {
		n := shortLen - eccLen
		if i >= numShort {
			n++
		}
		dat := append([]byte(nil), data[k:k+n]...)
		k += n
		ecc := rsRemainder(dat, divisor)
		if i < numShort {
			dat = append(dat, 0)
		}
		blocks = append(blocks, append(dat, ecc...))
	}
	var out []byte
	for i := range blocks[0] {
		for j, b := range blocks {
			if i != shortLen-eccLen || j >= numShort {
				out = append(out, b[i])
			}
		}
	}
	return out
}

func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		hi := z >> 7
		z = (z << 1) ^ (hi * 0x1D)
		z ^= ((y >> uint(i)) & 1) * x
	}
	return z
}

func rsDivisor(degree int) []byte {
	result := make([]byte, degree)
	result[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range result {
			result[j] = gfMul(result[j], root)
			if j+1 < len(result) {
				result[j] ^= result[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return result
}

func rsRemainder(data, divisor []byte) []byte {
	result := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		copy(result, result[1:])
		result[len(result)-1] = 0
		for i, d := range divisor {
			result[i] ^= gfMul(d, factor)
		}
	}
	return result
}

func (q *QR) drawCodewords(data []byte) {
	i := 0
	for right := q.Size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < q.Size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = q.Size - 1 - vert
				}
				if !q.isFunc[y][x] && i < len(data)*8 {
					q.dark[y][x] = (data[i>>3]>>(7-uint(i&7)))&1 == 1
					i++
				}
			}
		}
	}
}

func (q *QR) applyMask(mask int) {
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			var inv bool
			switch mask {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (x/3+y/2)%2 == 0
			case 5:
				inv = x*y%2+x*y%3 == 0
			case 6:
				inv = (x*y%2+x*y%3)%2 == 0
			case 7:
				inv = ((x+y)%2+x*y%3)%2 == 0
			}
			if inv && !q.isFunc[y][x] {
				q.dark[y][x] = !q.dark[y][x]
			}
		}
	}
}

// penalty scores runs, 2x2 blocks and imbalance (rules 1, 2 and 4). Rule 3, finder-like patterns, is left out: any mask is a valid code,
// and the rule only steers the choice.
func (q *QR) penalty() int {
	p := 0
	for y := 0; y < q.Size; y++ {
		for _, horizontal := range []bool{true, false} {
			run, last := 0, false
			for x := 0; x < q.Size; x++ {
				d := q.dark[y][x]
				if !horizontal {
					d = q.dark[x][y]
				}
				if x > 0 && d == last {
					run++
				} else {
					if run >= 5 {
						p += run - 2
					}
					run = 1
				}
				last = d
			}
			if run >= 5 {
				p += run - 2
			}
		}
	}
	dark := 0
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			if q.dark[y][x] {
				dark++
			}
			if x+1 < q.Size && y+1 < q.Size {
				c := q.dark[y][x]
				if c == q.dark[y][x+1] && c == q.dark[y+1][x] && c == q.dark[y+1][x+1] {
					p += 3
				}
			}
		}
	}
	total := q.Size * q.Size
	k := (abs(dark*20-total*10) + total - 1) / total
	return p + (k-1)*10
}

// SVG draws the code with a four-module quiet zone, as ISO/IEC 18004 requires.
func (q *QR) SVG() string {
	n := q.Size + 8
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := 0; y < q.Size; y++ {
		for x := 0; x < q.Size; x++ {
			if q.dark[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+4, y+4)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// Image renders the code at scale pixels per module, with the quiet zone.
func (q *QR) Image(scale int) (image.Image, error) {
	if scale < 1 {
		return nil, errors.New("scale must be at least 1")
	}
	n := (q.Size + 8) * scale
	img := image.NewGray(image.Rect(0, 0, n, n))
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			c := color.Gray{Y: 255}
			if q.Dark(px/scale-4, py/scale-4) {
				c = color.Gray{Y: 0}
			}
			img.SetGray(px, py, c)
		}
	}
	return img, nil
}
