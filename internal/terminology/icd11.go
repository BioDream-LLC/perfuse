package terminology

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ICD-11: the WHO's release files and the WHO ICD API.
//
// The MMS "simple tabulation" the ICD-11 browser offers for download is one tab-separated row per entity, with a header;
// the columns this reads are found by name (Code, Title, ClassKind), because their order has changed between releases.
// Titles carry leading dashes for depth ("- - Cholera"), which are removed. Chapters and blocks have no code and are left
// out unless blocks is asked for.

func readICD11Tabulation(r io.Reader, blocks bool, add func(code, display string, active bool) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	codeCol, titleCol, kindCol, blockCol := -1, -1, -1, -1
	header := true
	for sc.Scan() {
		f := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		if header {
			header = false
			for i, h := range f {
				switch strings.ToLower(strings.Trim(strings.TrimPrefix(h, "\ufeff"), `" `)) {
				case "code":
					codeCol = i
				case "title":
					titleCol = i
				case "classkind":
					kindCol = i
				case "blockid":
					blockCol = i
				}
			}
			if codeCol < 0 || titleCol < 0 {
				return errors.New("this is not the ICD-11 MMS simple tabulation: its header should name Code and Title columns (tab-separated)")
			}
			continue
		}
		get := func(i int) string {
			if i < 0 || i >= len(f) {
				return ""
			}
			return strings.Trim(f[i], `" `)
		}
		code := get(codeCol)
		title := strings.TrimSpace(strings.TrimLeft(get(titleCol), "- "))
		if code == "" && blocks {
			code = get(blockCol)
		}
		if code == "" || title == "" {
			continue
		}
		if k := strings.ToLower(get(kindCol)); !blocks && (k == "chapter" || k == "block") {
			continue
		}
		if err := add(code, title, true); err != nil {
			return err
		}
	}
	return sc.Err()
}

// ICD11API looks codes up in the WHO ICD API when they are not imported: a client registered at icd.who.int, a release
// (2025-01), and the language asked for. Answers are kept for a day. With a BaseURL and no client, it asks a local copy of
// the API (the WHO's ICD-API container), which takes no token.
type ICD11API struct {
	ClientID, ClientSecret string
	Release                string
	// TokenURL and BaseURL default to the WHO's; tests point them elsewhere.
	TokenURL, BaseURL string
	Client            *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	cache   map[string]icd11Answer
}

type icd11Answer struct {
	title string
	found bool
	at    time.Time
}

const (
	whoTokenURL = "https://icdaccessmanagement.who.int/connect/token"
	whoBaseURL  = "https://id.who.int"
)

func (a *ICD11API) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (a *ICD11API) bearer(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.expires) {
		return a.token, nil
	}
	tu := a.TokenURL
	if tu == "" {
		tu = whoTokenURL
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"icdapi_access"},
		"client_id": {a.ClientID}, "client_secret": {a.ClientSecret}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tu, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("the WHO ICD API's sign-in: %w", err)
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("the WHO ICD API refused the client ID and secret (%s)", res.Status)
	}
	if tok.ExpiresIn <= 60 {
		tok.ExpiresIn = 3600
	}
	a.token, a.expires = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn-60)*time.Second)
	return a.token, nil
}

func (a *ICD11API) get(ctx context.Context, u, lang string, out any) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if a.ClientID != "" {
		tok, err := a.bearer(ctx)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("API-Version", "v2")
	req.Header.Set("Accept-Language", lang)
	res, err := a.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil
	}
	return res.StatusCode, json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
}

// Lookup finds a code's title in the release, in lang (en when empty).
func (a *ICD11API) Lookup(ctx context.Context, code, lang string) (title string, found bool, err error) {
	if lang == "" {
		lang = "en"
	}
	rel := a.Release
	if rel == "" {
		rel = "2025-01"
	}
	key := rel + "|" + lang + "|" + code
	a.mu.Lock()
	if c, ok := a.cache[key]; ok && time.Since(c.at) < 24*time.Hour {
		a.mu.Unlock()
		return c.title, c.found, nil
	}
	a.mu.Unlock()
	base := a.BaseURL
	if base == "" {
		base = whoBaseURL
	}
	var info struct {
		Code   string `json:"code"`
		StemID string `json:"stemId"`
	}
	st, err := a.get(ctx, base+"/icd/release/11/"+url.PathEscape(rel)+"/mms/codeinfo/"+url.PathEscape(code), lang, &info)
	if err != nil {
		return "", false, err
	}
	if st == http.StatusNotFound || (st == http.StatusOK && info.StemID == "") {
		a.remember(key, "", false)
		return "", false, nil
	}
	if st != http.StatusOK {
		return "", false, fmt.Errorf("the WHO ICD API answered %d", st)
	}
	// The stem is the entity the code stands for; its title is in the language asked for when the WHO has it.
	stem := strings.Replace(info.StemID, "http://id.who.int", base, 1)
	var ent struct {
		Title struct {
			Value string `json:"@value"`
		} `json:"title"`
	}
	if st, err := a.get(ctx, stem, lang, &ent); err != nil || st != http.StatusOK {
		if err == nil {
			err = fmt.Errorf("the WHO ICD API answered %d for %s", st, info.StemID)
		}
		return "", false, err
	}
	title = ent.Title.Value
	// A postcoordinated cluster (2C25.0&XK9K, or stems joined by /) was checked whole by the API, which refuses a
	// combination the classification does not allow. Its stem's title alone would read as a different diagnosis, so the
	// other codes' titles are added: "Adenocarcinoma of bronchus or lung (Right)".
	if parts := strings.FieldsFunc(code, func(r rune) bool { return r == '&' || r == '/' }); len(parts) > 1 {
		var extra []string
		for _, p := range parts[1:] {
			t, ok, err := a.Lookup(ctx, p, lang)
			if err != nil {
				return "", false, err
			}
			if ok {
				extra = append(extra, t)
			}
		}
		if len(extra) > 0 {
			title += " (" + strings.Join(extra, "; ") + ")"
		}
	}
	a.remember(key, title, true)
	return title, true, nil
}

func (a *ICD11API) remember(key, title string, found bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil {
		a.cache = map[string]icd11Answer{}
	}
	if len(a.cache) > 50000 {
		a.cache = map[string]icd11Answer{}
	}
	a.cache[key] = icd11Answer{title, found, time.Now()}
}
