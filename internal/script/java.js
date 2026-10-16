// JavaScript equivalents of the Java classes Mirth scripts use most, so a migrated script that calls them runs unchanged.
//
// Perfuse has no Java runtime. These are not Java: they are small JavaScript classes with the same names and methods, enough
// for the way channel scripts use them (collections, string building, number parsing, dates, regular expressions, base64,
// URL encoding, Apache Commons StringUtils). Anything not here is still refused by name, at the line that reaches for it.
//
// Installed once per script runtime, before the shared library. The Go side supplies __jhelp (UTF-8, base64, dates, UUIDs) and
// reads the table __javaClasses, keyed by full class name.
(function (H) {
  'use strict'
  // The built-ins, held here so a script that rebinds a global name cannot make these classes call themselves.
  var Map = globalThis.Map
  var Date = globalThis.Date
  var Math = globalThis.Math
  var String = globalThis.String
  var Number = globalThis.Number
  var RegExp = globalThis.RegExp
  var Array = globalThis.Array
  var Object = globalThis.Object
  var JSON = globalThis.JSON
  var classes = {}
  function def(names, value) {
    names.forEach(function (n) {
      classes[n] = value
    })
  }
  function NumberFormatException(msg) {
    var e = new Error('java.lang.NumberFormatException: ' + msg)
    e.name = 'NumberFormatException'
    return e
  }
  function jstr(v) {
    if (v === null || v === undefined) return 'null'
    if (v instanceof JList || v instanceof JSet) return v.toString()
    if (v instanceof JMap) return v.toString()
    return String(v)
  }
  function eq(a, b) {
    if (a === b) return true
    if (a === null || b === null || a === undefined || b === undefined) return false
    if (typeof a === 'object' && typeof a.equals === 'function') return a.equals(b)
    return String(a) === String(b) && typeof a === typeof b
  }
  function key(k) {
    // Java maps key strings by value; a JavaScript Map keys String objects by identity, so strings are normalised.
    if (k instanceof JString) return k.toString()
    if (typeof k === 'object' && k !== null && typeof k.toString === 'function' && k.constructor === String) return String(k)
    return k
  }

  // ---------------------------------------------------------------- iteration
  function JIterator(arr) {
    this._a = arr
    this._i = 0
  }
  JIterator.prototype.hasNext = function () {
    return this._i < this._a.length
  }
  JIterator.prototype.next = function () {
    if (this._i >= this._a.length) throw new Error('java.util.NoSuchElementException')
    return this._a[this._i++]
  }

  // ---------------------------------------------------------------- lists
  function JList(init) {
    if (!(this instanceof JList)) return new JList(init)
    this._a = []
    if (init && typeof init.toArray === 'function') this._a = init.toArray()
    else if (Array.isArray(init)) this._a = init.slice()
  }
  JList.prototype.add = function (i, v) {
    if (arguments.length === 2) {
      this._a.splice(i, 0, v)
      return
    }
    this._a.push(i)
    return true
  }
  JList.prototype.addAll = function (c) {
    var a = c && typeof c.toArray === 'function' ? c.toArray() : c || []
    for (var i = 0; i < a.length; i++) this._a.push(a[i])
    return a.length > 0
  }
  JList.prototype.get = function (i) {
    if (i < 0 || i >= this._a.length) throw new Error('java.lang.IndexOutOfBoundsException: Index ' + i + ' out of bounds for length ' + this._a.length)
    return this._a[i]
  }
  JList.prototype.set = function (i, v) {
    var old = this.get(i)
    this._a[i] = v
    return old
  }
  JList.prototype.size = function () {
    return this._a.length
  }
  JList.prototype.isEmpty = function () {
    return this._a.length === 0
  }
  JList.prototype.indexOf = function (v) {
    for (var i = 0; i < this._a.length; i++) if (eq(this._a[i], v)) return i
    return -1
  }
  JList.prototype.lastIndexOf = function (v) {
    for (var i = this._a.length - 1; i >= 0; i--) if (eq(this._a[i], v)) return i
    return -1
  }
  JList.prototype.contains = function (v) {
    return this.indexOf(v) >= 0
  }
  JList.prototype.remove = function (v) {
    // Java decides by type: an int removes by index, anything else by value.
    if (typeof v === "number" && Math.floor(v) === v) {
      this.get(v)
      return this._a.splice(v, 1)[0]
    }
    var i = this.indexOf(v)
    if (i < 0) return false
    this._a.splice(i, 1)
    return true
  }
  JList.prototype.clear = function () {
    this._a = []
  }
  JList.prototype.toArray = function () {
    return this._a.slice()
  }
  JList.prototype.iterator = function () {
    return new JIterator(this._a)
  }
  JList.prototype.forEach = function (fn) {
    this._a.forEach(function (v) {
      fn(v)
    })
  }
  JList.prototype.subList = function (from, to) {
    return new JList(this._a.slice(from, to))
  }
  JList.prototype.sort = function (cmp) {
    this._a.sort(cmp ? (typeof cmp === 'function' ? cmp : cmp.compare.bind(cmp)) : undefined)
  }
  JList.prototype.equals = function (o) {
    if (!(o instanceof JList) || o.size() !== this.size()) return false
    for (var i = 0; i < this._a.length; i++) if (!eq(this._a[i], o._a[i])) return false
    return true
  }
  JList.prototype.toString = function () {
    return '[' + this._a.map(jstr).join(', ') + ']'
  }
  JList.prototype[Symbol.iterator] = function () {
    return this._a[Symbol.iterator]()
  }
  def(['java.util.ArrayList', 'java.util.LinkedList', 'java.util.Vector', 'java.util.List', 'java.util.Collection'], JList)

  // ---------------------------------------------------------------- sets
  function JSet(init, sorted) {
    if (!(this instanceof JSet)) return new JSet(init, sorted)
    this._m = new Map()
    this._sorted = !!sorted
    if (init) this.addAll(init)
  }
  JSet.prototype._keys = function () {
    var k = Array.from(this._m.values())
    if (this._sorted) k.sort(function (a, b) { return a < b ? -1 : a > b ? 1 : 0 })
    return k
  }
  JSet.prototype.add = function (v) {
    var k = key(v)
    if (this._m.has(k)) return false
    this._m.set(k, v)
    return true
  }
  JSet.prototype.addAll = function (c) {
    var a = c && typeof c.toArray === 'function' ? c.toArray() : c || []
    var changed = false
    for (var i = 0; i < a.length; i++) changed = this.add(a[i]) || changed
    return changed
  }
  JSet.prototype.contains = function (v) {
    return this._m.has(key(v))
  }
  JSet.prototype.remove = function (v) {
    return this._m.delete(key(v))
  }
  JSet.prototype.size = function () {
    return this._m.size
  }
  JSet.prototype.isEmpty = function () {
    return this._m.size === 0
  }
  JSet.prototype.clear = function () {
    this._m.clear()
  }
  JSet.prototype.toArray = function () {
    return this._keys()
  }
  JSet.prototype.iterator = function () {
    return new JIterator(this._keys())
  }
  JSet.prototype.forEach = function (fn) {
    this._keys().forEach(function (v) {
      fn(v)
    })
  }
  JSet.prototype.toString = function () {
    return '[' + this._keys().map(jstr).join(', ') + ']'
  }
  JSet.prototype[Symbol.iterator] = function () {
    return this._keys()[Symbol.iterator]()
  }
  def(['java.util.HashSet', 'java.util.LinkedHashSet', 'java.util.Set'], JSet)
  def(['java.util.TreeSet', 'java.util.SortedSet'], function TreeSet(init) {
    return new JSet(init, true)
  })

  // ---------------------------------------------------------------- maps
  function JEntry(k, v) {
    this._k = k
    this._v = v
  }
  JEntry.prototype.getKey = function () {
    return this._k
  }
  JEntry.prototype.getValue = function () {
    return this._v
  }
  JEntry.prototype.toString = function () {
    return jstr(this._k) + '=' + jstr(this._v)
  }
  function JMap(init, sorted) {
    if (!(this instanceof JMap)) return new JMap(init, sorted)
    this._m = new Map()
    this._sorted = !!sorted
    if (init && typeof init.entrySet === 'function') this.putAll(init)
  }
  JMap.prototype._entries = function () {
    var e = Array.from(this._m.values())
    if (this._sorted) e.sort(function (a, b) { return a.k < b.k ? -1 : a.k > b.k ? 1 : 0 })
    return e
  }
  JMap.prototype.put = function (k, v) {
    var kk = key(k)
    var old = this._m.has(kk) ? this._m.get(kk).v : null
    this._m.set(kk, { k: k, v: v })
    return old
  }
  JMap.prototype.get = function (k) {
    var e = this._m.get(key(k))
    return e ? e.v : null
  }
  JMap.prototype.getOrDefault = function (k, d) {
    return this.containsKey(k) ? this.get(k) : d
  }
  JMap.prototype.putIfAbsent = function (k, v) {
    if (this.containsKey(k)) return this.get(k)
    this.put(k, v)
    return null
  }
  JMap.prototype.containsKey = function (k) {
    return this._m.has(key(k))
  }
  JMap.prototype.containsValue = function (v) {
    return this._entries().some(function (e) {
      return eq(e.v, v)
    })
  }
  JMap.prototype.remove = function (k) {
    var kk = key(k)
    var e = this._m.get(kk)
    this._m.delete(kk)
    return e ? e.v : null
  }
  JMap.prototype.size = function () {
    return this._m.size
  }
  JMap.prototype.isEmpty = function () {
    return this._m.size === 0
  }
  JMap.prototype.clear = function () {
    this._m.clear()
  }
  JMap.prototype.putAll = function (o) {
    var self = this
    if (o && typeof o.entrySet === 'function') {
      o.entrySet().forEach(function (e) {
        self.put(e.getKey(), e.getValue())
      })
    } else if (o) {
      Object.keys(o).forEach(function (k) {
        self.put(k, o[k])
      })
    }
  }
  JMap.prototype.keySet = function () {
    return new JSet(this._entries().map(function (e) { return e.k }), false)
  }
  JMap.prototype.values = function () {
    return new JList(this._entries().map(function (e) { return e.v }))
  }
  JMap.prototype.entrySet = function () {
    var s = new JSet()
    this._entries().forEach(function (e, i) {
      var en = new JEntry(e.k, e.v)
      s._m.set(i, en)
    })
    return s
  }
  JMap.prototype.forEach = function (fn) {
    this._entries().forEach(function (e) {
      fn(e.k, e.v)
    })
  }
  JMap.prototype.toString = function () {
    return '{' + this._entries().map(function (e) { return jstr(e.k) + '=' + jstr(e.v) }).join(', ') + '}'
  }
  def(['java.util.HashMap', 'java.util.LinkedHashMap', 'java.util.Hashtable', 'java.util.Map', 'java.util.Properties',
    'java.util.concurrent.ConcurrentHashMap'], JMap)
  def(['java.util.TreeMap', 'java.util.SortedMap'], function TreeMap(init) {
    return new JMap(init, true)
  })

  def(['java.util.Arrays'], {
    asList: function () {
      var a = arguments.length === 1 && Array.isArray(arguments[0]) ? arguments[0] : Array.prototype.slice.call(arguments)
      return new JList(a)
    },
    toString: function (a) {
      return '[' + (a || []).map(jstr).join(', ') + ']'
    },
    sort: function (a) {
      a.sort()
    },
  })
  def(['java.util.Collections'], {
    emptyList: function () { return new JList() },
    emptyMap: function () { return new JMap() },
    emptySet: function () { return new JSet() },
    singletonList: function (v) { return new JList([v]) },
    sort: function (l, cmp) { l.sort(cmp) },
    reverse: function (l) { l._a.reverse() },
    unmodifiableList: function (l) { return l },
    unmodifiableMap: function (m) { return m },
    unmodifiableSet: function (s) { return s },
  })

  // ---------------------------------------------------------------- strings
  function JString(v, charset) {
    if (!(this instanceof JString)) return new JString(v, charset)
    if (Array.isArray(v) || (v && typeof v.length === 'number' && typeof v !== 'string' && v.BYTES_PER_ELEMENT === 1)) v = H.utf8Decode(Array.from(v))
    this._s = v === undefined ? '' : v === null ? 'null' : String(v)
  }
  var S = JString.prototype
  S.toString = S.valueOf = function () {
    return this._s
  }
  S.length = function () {
    return this._s.length
  }
  S.equals = function (o) {
    return o !== null && o !== undefined && String(o) === this._s
  }
  S.equalsIgnoreCase = function (o) {
    return o !== null && o !== undefined && String(o).toLowerCase() === this._s.toLowerCase()
  }
  S.compareTo = function (o) {
    var b = String(o)
    return this._s < b ? -1 : this._s > b ? 1 : 0
  }
  S.isEmpty = function () {
    return this._s.length === 0
  }
  S.trim = function () {
    return new JString(this._s.trim())
  }
  S.toUpperCase = function () {
    return new JString(this._s.toUpperCase())
  }
  S.toLowerCase = function () {
    return new JString(this._s.toLowerCase())
  }
  S.charAt = function (i) {
    return this._s.charAt(i)
  }
  S.substring = function (a, b) {
    return new JString(this._s.substring(a, b))
  }
  S.indexOf = function (x, from) {
    return this._s.indexOf(String(x), from)
  }
  S.lastIndexOf = function (x) {
    return this._s.lastIndexOf(String(x))
  }
  S.contains = function (x) {
    return this._s.indexOf(String(x)) >= 0
  }
  S.startsWith = function (x) {
    return this._s.indexOf(String(x)) === 0
  }
  S.endsWith = function (x) {
    x = String(x)
    return this._s.length >= x.length && this._s.slice(this._s.length - x.length) === x
  }
  S.concat = function (x) {
    return new JString(this._s + String(x))
  }
  // Java's replace is literal; replaceAll, replaceFirst, split and matches take a regular expression.
  S.replace = function (a, b) {
    return new JString(this._s.split(String(a)).join(String(b)))
  }
  S.replaceAll = function (re, b) {
    return new JString(this._s.replace(new RegExp(String(re), 'g'), javaReplacement(b)))
  }
  S.replaceFirst = function (re, b) {
    return new JString(this._s.replace(new RegExp(String(re)), javaReplacement(b)))
  }
  S.matches = function (re) {
    return new RegExp('^(?:' + String(re) + ')$').test(this._s)
  }
  S.split = function (re, limit) {
    var parts = this._s.split(new RegExp(String(re)))
    if (limit > 0 && parts.length > limit) parts = parts.slice(0, limit - 1).concat([parts.slice(limit - 1).join('')])
    if (!(limit < 0)) while (parts.length > 1 && parts[parts.length - 1] === '') parts.pop()
    return parts
  }
  S.getBytes = function () {
    return H.utf8Encode(this._s)
  }
  S.toCharArray = function () {
    return this._s.split('')
  }
  S.hashCode = function () {
    var h = 0
    for (var i = 0; i < this._s.length; i++) h = (Math.imul(31, h) + this._s.charCodeAt(i)) | 0
    return h
  }
  function javaReplacement(b) {
    // Java writes groups as $1 and a literal dollar as \$; JavaScript writes $1 and $$.
    return String(b).replace(/\\\$/g, '$$$$')
  }
  JString.valueOf = function (v) {
    return jstr(v)
  }
  JString.join = function (sep, parts) {
    var a = parts && typeof parts.toArray === 'function' ? parts.toArray() : Array.isArray(parts) ? parts : Array.prototype.slice.call(arguments, 1)
    return a.map(jstr).join(String(sep))
  }
  JString.format = function (fmt) {
    var args = Array.prototype.slice.call(arguments, 1)
    var n = 0
    return String(fmt).replace(/%(-)?(0)?(\d+)?(?:\.(\d+))?([sdfxXn%])/g, function (m, left, zero, width, prec, conv) {
      if (conv === '%') return '%'
      if (conv === 'n') return '\n'
      var v = args[n++]
      var out
      if (conv === 'd') out = String(Math.trunc(Number(v)))
      else if (conv === 'f') out = Number(v).toFixed(prec === undefined ? 6 : Number(prec))
      else if (conv === 'x' || conv === 'X') out = (Number(v) >>> 0).toString(16)
      else out = jstr(v)
      if (conv === 'X') out = out.toUpperCase()
      if (conv === 's' && prec !== undefined) out = out.slice(0, Number(prec))
      var w = width ? Number(width) : 0
      while (out.length < w) out = left ? out + ' ' : (zero && conv !== 's' ? '0' : ' ') + out
      return out
    })
  }
  def(['java.lang.String'], JString)

  function JBuilder(init) {
    if (!(this instanceof JBuilder)) return new JBuilder(init)
    this._s = typeof init === 'number' || init === undefined ? '' : jstr(init)
  }
  var B = JBuilder.prototype
  B.append = function (v) {
    this._s += jstr(v)
    return this
  }
  B.insert = function (i, v) {
    this._s = this._s.slice(0, i) + jstr(v) + this._s.slice(i)
    return this
  }
  B.toString = function () {
    return this._s
  }
  B.length = function () {
    return this._s.length
  }
  B.charAt = function (i) {
    return this._s.charAt(i)
  }
  B.indexOf = function (x) {
    return this._s.indexOf(String(x))
  }
  B.reverse = function () {
    this._s = this._s.split('').reverse().join('')
    return this
  }
  B.setLength = function (n) {
    this._s = n <= this._s.length ? this._s.slice(0, n) : this._s + new Array(n - this._s.length + 1).join('\u0000')
  }
  B.deleteCharAt = function (i) {
    this._s = this._s.slice(0, i) + this._s.slice(i + 1)
    return this
  }
  B.delete = function (a, b) {
    this._s = this._s.slice(0, a) + this._s.slice(b)
    return this
  }
  B.replace = function (a, b, str) {
    this._s = this._s.slice(0, a) + String(str) + this._s.slice(b)
    return this
  }
  B.setCharAt = function (i, c) {
    this._s = this._s.slice(0, i) + String(c) + this._s.slice(i + 1)
  }
  def(['java.lang.StringBuilder', 'java.lang.StringBuffer'], JBuilder)

  // ---------------------------------------------------------------- numbers
  function parseIntStrict(s, radix) {
    var t = String(s).trim()
    var re = radix === 16 ? /^[+-]?[0-9a-fA-F]+$/ : /^[+-]?\d+$/
    if (s === null || s === undefined || !re.test(t)) throw NumberFormatException('For input string: "' + s + '"')
    return parseInt(t, radix || 10)
  }
  function parseFloatStrict(s) {
    var t = String(s).trim()
    if (s === null || s === undefined || t === '' || isNaN(Number(t))) throw NumberFormatException('For input string: "' + s + '"')
    return Number(t)
  }
  var Int = function (v) {
    return parseIntStrict(v)
  }
  Int.parseInt = parseIntStrict
  Int.valueOf = function (v, r) {
    return typeof v === 'number' ? Math.trunc(v) : parseIntStrict(v, r)
  }
  Int.toString = function (v, r) {
    return Number(v).toString(r || 10)
  }
  Int.MAX_VALUE = 2147483647
  Int.MIN_VALUE = -2147483648
  def(['java.lang.Integer', 'java.lang.Short', 'java.lang.Byte'], Int)
  var Lng = function (v) {
    return parseIntStrict(v)
  }
  Lng.parseLong = parseIntStrict
  Lng.valueOf = Int.valueOf
  Lng.toString = Int.toString
  Lng.MAX_VALUE = 9007199254740991
  Lng.MIN_VALUE = -9007199254740991
  def(['java.lang.Long'], Lng)
  var Dbl = function (v) {
    return parseFloatStrict(v)
  }
  Dbl.parseDouble = Dbl.parseFloat = parseFloatStrict
  Dbl.valueOf = function (v) {
    return typeof v === 'number' ? v : parseFloatStrict(v)
  }
  Dbl.toString = function (v) {
    return String(Number(v))
  }
  Dbl.isNaN = function (v) {
    return isNaN(v)
  }
  def(['java.lang.Double', 'java.lang.Float'], Dbl)
  var Bool = function (v) {
    return String(v).toLowerCase() === 'true'
  }
  Bool.parseBoolean = Bool
  Bool.valueOf = function (v) {
    return typeof v === 'boolean' ? v : Bool(v)
  }
  Bool.toString = function (v) {
    return String(!!v)
  }
  Bool.TRUE = true
  Bool.FALSE = false
  def(['java.lang.Boolean'], Bool)
  var JMath = Object.create(Math)
  // Java's round is half-up towards positive infinity, as JavaScript's is; the rest are the same functions.
  JMath.round = function (v) {
    return Math.floor(v + 0.5)
  }
  def(['java.lang.Math'], JMath)
  def(['java.math.BigDecimal'], function BigDecimal(v) {
    var n = parseFloatStrict(v)
    return {
      doubleValue: function () { return n },
      intValue: function () { return Math.trunc(n) },
      toString: function () { return String(v).trim() },
      toPlainString: function () { return String(v).trim() },
      setScale: function (s) { var f = Number(n.toFixed(s)); return BigDecimal(f.toFixed(s)) },
      add: function (o) { return BigDecimal(String(n + Number(o.doubleValue ? o.doubleValue() : o))) },
      subtract: function (o) { return BigDecimal(String(n - Number(o.doubleValue ? o.doubleValue() : o))) },
      multiply: function (o) { return BigDecimal(String(n * Number(o.doubleValue ? o.doubleValue() : o))) },
      compareTo: function (o) { var m = Number(o.doubleValue ? o.doubleValue() : o); return n < m ? -1 : n > m ? 1 : 0 },
    }
  })

  // ---------------------------------------------------------------- system
  def(['java.lang.System'], {
    currentTimeMillis: function () {
      return Date.now()
    },
    nanoTime: function () {
      return Date.now() * 1e6
    },
    lineSeparator: function () {
      return '\n'
    },
    getProperty: function (k, d) {
      return { 'line.separator': '\n', 'file.separator': '/', 'path.separator': ':' }[k] || (d === undefined ? null : d)
    },
  })

  // ---------------------------------------------------------------- dates
  function JDate(v) {
    if (v === undefined) return new Date()
    return new Date(typeof v === 'number' ? v : Number(v))
  }
  def(['java.util.Date'], JDate)
  function JSimpleDateFormat(pattern) {
    if (!(this instanceof JSimpleDateFormat)) return new JSimpleDateFormat(pattern)
    this._p = String(pattern || 'yyyy-MM-dd HH:mm:ss')
  }
  JSimpleDateFormat.prototype.format = function (d) {
    if (d instanceof JCalendar) d = d.getTime()
    return H.dateFormat(this._p, d === undefined ? new Date() : d instanceof Date ? d : new Date(Number(d)))
  }
  JSimpleDateFormat.prototype.parse = function (text) {
    return H.dateParse(this._p, String(text))
  }
  JSimpleDateFormat.prototype.setLenient = function () {}
  JSimpleDateFormat.prototype.setTimeZone = function () {}
  JSimpleDateFormat.prototype.toPattern = function () {
    return this._p
  }
  def(['java.text.SimpleDateFormat', 'java.text.DateFormat'], JSimpleDateFormat)

  function JCalendar(d) {
    this._d = d ? new Date(d.getTime()) : new Date()
  }
  var C = { ERA: 0, YEAR: 1, MONTH: 2, WEEK_OF_YEAR: 3, DATE: 5, DAY_OF_MONTH: 5, DAY_OF_YEAR: 6, DAY_OF_WEEK: 7, AM_PM: 9,
    HOUR: 10, HOUR_OF_DAY: 11, MINUTE: 12, SECOND: 13, MILLISECOND: 14 }
  JCalendar.prototype.getTime = function () {
    return new Date(this._d.getTime())
  }
  JCalendar.prototype.setTime = function (d) {
    this._d = new Date(d.getTime())
  }
  JCalendar.prototype.getTimeInMillis = function () {
    return this._d.getTime()
  }
  JCalendar.prototype.setTimeInMillis = function (ms) {
    this._d = new Date(ms)
  }
  JCalendar.prototype.get = function (f) {
    var d = this._d
    switch (f) {
      case C.YEAR: return d.getFullYear()
      case C.MONTH: return d.getMonth()
      case C.DATE: return d.getDate()
      case C.DAY_OF_WEEK: return d.getDay() + 1
      case C.HOUR_OF_DAY: return d.getHours()
      case C.HOUR: return d.getHours() % 12
      case C.MINUTE: return d.getMinutes()
      case C.SECOND: return d.getSeconds()
      case C.MILLISECOND: return d.getMilliseconds()
      case C.DAY_OF_YEAR: return Math.floor((d - new Date(d.getFullYear(), 0, 1)) / 86400000) + 1
    }
    throw new Error('java.util.Calendar field ' + f + ' is not supported here')
  }
  JCalendar.prototype.set = function (f, v) {
    var d = this._d
    if (arguments.length >= 3) {
      d.setFullYear(arguments[0], arguments[1], arguments[2])
      if (arguments.length >= 5) d.setHours(arguments[3], arguments[4], arguments.length >= 6 ? arguments[5] : d.getSeconds())
      return
    }
    switch (f) {
      case C.YEAR: d.setFullYear(v); return
      case C.MONTH: d.setMonth(v); return
      case C.DATE: d.setDate(v); return
      case C.HOUR_OF_DAY: d.setHours(v); return
      case C.MINUTE: d.setMinutes(v); return
      case C.SECOND: d.setSeconds(v); return
      case C.MILLISECOND: d.setMilliseconds(v); return
    }
    throw new Error('java.util.Calendar field ' + f + ' is not supported here')
  }
  JCalendar.prototype.add = function (f, n) {
    var d = this._d
    switch (f) {
      case C.YEAR: d.setFullYear(d.getFullYear() + n); return
      case C.MONTH: d.setMonth(d.getMonth() + n); return
      case C.DATE: case C.DAY_OF_YEAR: case C.DAY_OF_WEEK: d.setDate(d.getDate() + n); return
      case C.WEEK_OF_YEAR: d.setDate(d.getDate() + 7 * n); return
      case C.HOUR: case C.HOUR_OF_DAY: d.setHours(d.getHours() + n); return
      case C.MINUTE: d.setMinutes(d.getMinutes() + n); return
      case C.SECOND: d.setSeconds(d.getSeconds() + n); return
      case C.MILLISECOND: d.setMilliseconds(d.getMilliseconds() + n); return
    }
    throw new Error('java.util.Calendar field ' + f + ' is not supported here')
  }
  var Calendar = { getInstance: function () { return new JCalendar() } }
  Object.keys(C).forEach(function (k) {
    Calendar[k] = C[k]
  })
  def(['java.util.Calendar', 'java.util.GregorianCalendar'], Calendar)

  def(['java.util.UUID'], {
    randomUUID: function () {
      var u = H.uuid()
      return { toString: function () { return u } }
    },
  })

  // ---------------------------------------------------------------- regular expressions
  function JPattern(re, flags) {
    this._re = String(re)
    this._f = (flags & 2 ? 'i' : '') + (flags & 8 ? 'm' : '') + (flags & 32 ? 's' : '')
  }
  JPattern.prototype.matcher = function (s) {
    return new JMatcher(this, String(s))
  }
  JPattern.prototype.pattern = function () {
    return this._re
  }
  JPattern.prototype.split = function (s) {
    return String(s).split(new RegExp(this._re, this._f))
  }
  function JMatcher(p, s) {
    this._p = p
    this._s = s
    this._g = new RegExp(p._re, 'g' + p._f)
    this._m = null
  }
  JMatcher.prototype.find = function () {
    this._m = this._g.exec(this._s)
    if (this._m && this._m[0] === '') this._g.lastIndex++
    return this._m !== null
  }
  JMatcher.prototype.matches = function () {
    this._m = new RegExp('^(?:' + this._p._re + ')$', this._p._f).exec(this._s)
    return this._m !== null
  }
  JMatcher.prototype.lookingAt = function () {
    this._m = new RegExp('^(?:' + this._p._re + ')', this._p._f).exec(this._s)
    return this._m !== null
  }
  JMatcher.prototype.group = function (n) {
    if (!this._m) throw new Error('java.lang.IllegalStateException: No match found')
    var v = this._m[n || 0]
    return v === undefined ? null : v
  }
  JMatcher.prototype.groupCount = function () {
    return this._m ? this._m.length - 1 : 0
  }
  JMatcher.prototype.start = function () {
    return this._m ? this._m.index : -1
  }
  JMatcher.prototype.end = function () {
    return this._m ? this._m.index + this._m[0].length : -1
  }
  JMatcher.prototype.replaceAll = function (b) {
    return this._s.replace(new RegExp(this._p._re, 'g' + this._p._f), javaReplacement(b))
  }
  JMatcher.prototype.replaceFirst = function (b) {
    return this._s.replace(new RegExp(this._p._re, this._p._f), javaReplacement(b))
  }
  def(['java.util.regex.Pattern'], {
    compile: function (re, flags) {
      return new JPattern(re, flags || 0)
    },
    matches: function (re, s) {
      return new RegExp('^(?:' + String(re) + ')$').test(String(s))
    },
    quote: function (s) {
      return String(s).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    },
    CASE_INSENSITIVE: 2,
    MULTILINE: 8,
    DOTALL: 32,
  })

  // ---------------------------------------------------------------- encoding
  function bytesOf(v) {
    if (v instanceof JString) return H.utf8Encode(v.toString())
    if (typeof v === 'string') return H.utf8Encode(v)
    return Array.from(v || [])
  }
  var B64 = {
    encodeBase64String: function (v) {
      return H.b64Encode(bytesOf(v))
    },
    encodeBase64: function (v) {
      return H.utf8Encode(H.b64Encode(bytesOf(v)))
    },
    decodeBase64: function (v) {
      return H.b64Decode(typeof v === 'string' || v instanceof JString ? String(v) : H.utf8Decode(Array.from(v)))
    },
    isBase64: function (v) {
      return /^[A-Za-z0-9+/\r\n]*={0,2}$/.test(String(v))
    },
  }
  def(['org.apache.commons.codec.binary.Base64'], B64)
  def(['java.util.Base64'], {
    getEncoder: function () {
      return { encodeToString: function (v) { return H.b64Encode(bytesOf(v)) }, encode: function (v) { return H.utf8Encode(H.b64Encode(bytesOf(v))) } }
    },
    getDecoder: function () {
      return { decode: function (v) { return B64.decodeBase64(v) } }
    },
    getMimeEncoder: function () {
      return { encodeToString: function (v) { return H.b64Encode(bytesOf(v)).replace(/(.{76})/g, '$1\r\n') } }
    },
    getMimeDecoder: function () {
      return { decode: function (v) { return B64.decodeBase64(String(v).replace(/\s+/g, '')) } }
    },
  })
  def(['java.net.URLEncoder'], {
    encode: function (s) {
      return encodeURIComponent(String(s)).replace(/%20/g, '+').replace(/[!'()~]/g, function (c) {
        return '%' + c.charCodeAt(0).toString(16).toUpperCase()
      })
    },
  })
  def(['java.net.URLDecoder'], {
    decode: function (s) {
      return decodeURIComponent(String(s).replace(/\+/g, ' '))
    },
  })
  def(['java.nio.charset.StandardCharsets'], { UTF_8: 'UTF-8', US_ASCII: 'US-ASCII', ISO_8859_1: 'ISO-8859-1' })

  // ---------------------------------------------------------------- Apache Commons Lang StringUtils
  function blank(s) {
    return s === null || s === undefined || String(s).trim() === ''
  }
  function empty(s) {
    return s === null || s === undefined || String(s) === ''
  }
  function pad(s, n, c, left) {
    if (s === null || s === undefined) return null
    s = String(s)
    c = c === undefined || c === '' ? ' ' : String(c)
    var fill = ''
    while (s.length + fill.length < n) fill += c
    fill = fill.slice(0, Math.max(0, n - s.length))
    return left ? fill + s : s + fill
  }
  var SU = {
    EMPTY: '',
    isEmpty: empty,
    isNotEmpty: function (s) { return !empty(s) },
    isBlank: blank,
    isNotBlank: function (s) { return !blank(s) },
    trim: function (s) { return s === null || s === undefined ? null : String(s).trim() },
    trimToEmpty: function (s) { return s === null || s === undefined ? '' : String(s).trim() },
    trimToNull: function (s) { var t = s === null || s === undefined ? '' : String(s).trim(); return t === '' ? null : t },
    strip: function (s) { return s === null || s === undefined ? null : String(s).trim() },
    stripStart: function (s) { return s === null || s === undefined ? null : String(s).replace(/^\s+/, '') },
    stripEnd: function (s) { return s === null || s === undefined ? null : String(s).replace(/\s+$/, '') },
    defaultString: function (s, d) { return s === null || s === undefined ? (d === undefined ? '' : d) : String(s) },
    defaultIfEmpty: function (s, d) { return empty(s) ? d : s },
    defaultIfBlank: function (s, d) { return blank(s) ? d : s },
    leftPad: function (s, n, c) { return pad(s, n, c, true) },
    rightPad: function (s, n, c) { return pad(s, n, c, false) },
    center: function (s, n, c) { if (s === null || s === undefined) return null; s = String(s); var l = Math.floor((n - s.length) / 2); return pad(pad(s, s.length + Math.max(0, l), c, true), n, c, false) },
    repeat: function (s, n) { return new Array(Math.max(0, n) + 1).join(String(s)) },
    join: function (a, sep) {
      var arr = a && typeof a.toArray === 'function' ? a.toArray() : Array.isArray(a) ? a : []
      return arr.map(function (v) { return v === null || v === undefined ? '' : jstr(v) }).join(sep === undefined || sep === null ? '' : String(sep))
    },
    split: function (s, sep) {
      if (s === null || s === undefined) return null
      s = String(s)
      if (sep === undefined || sep === null) return s.trim() === '' ? [] : s.trim().split(/\s+/)
      var chars = String(sep)
      var out = []
      var cur = ''
      for (var i = 0; i < s.length; i++) {
        if (chars.indexOf(s[i]) >= 0) {
          if (cur !== '') out.push(cur)
          cur = ''
        } else cur += s[i]
      }
      if (cur !== '') out.push(cur)
      return out
    },
    contains: function (s, x) { return s !== null && s !== undefined && x !== null && x !== undefined && String(s).indexOf(String(x)) >= 0 },
    containsIgnoreCase: function (s, x) { return s !== null && s !== undefined && x !== null && x !== undefined && String(s).toLowerCase().indexOf(String(x).toLowerCase()) >= 0 },
    equals: function (a, b) { return (a === null || a === undefined) ? (b === null || b === undefined) : b !== null && b !== undefined && String(a) === String(b) },
    equalsIgnoreCase: function (a, b) { return (a === null || a === undefined) ? (b === null || b === undefined) : b !== null && b !== undefined && String(a).toLowerCase() === String(b).toLowerCase() },
    startsWith: function (s, p) { return s !== null && s !== undefined && String(s).indexOf(String(p)) === 0 },
    endsWith: function (s, p) { s = s === null || s === undefined ? null : String(s); p = String(p); return s !== null && s.length >= p.length && s.slice(s.length - p.length) === p },
    upperCase: function (s) { return s === null || s === undefined ? null : String(s).toUpperCase() },
    lowerCase: function (s) { return s === null || s === undefined ? null : String(s).toLowerCase() },
    capitalize: function (s) { return empty(s) ? s : String(s).charAt(0).toUpperCase() + String(s).slice(1) },
    uncapitalize: function (s) { return empty(s) ? s : String(s).charAt(0).toLowerCase() + String(s).slice(1) },
    replace: function (s, a, b) { return s === null || s === undefined ? null : empty(a) ? String(s) : String(s).split(String(a)).join(b === null || b === undefined ? '' : String(b)) },
    remove: function (s, a) { return s === null || s === undefined ? null : empty(a) ? String(s) : String(s).split(String(a)).join('') },
    deleteWhitespace: function (s) { return s === null || s === undefined ? null : String(s).replace(/\s+/g, '') },
    reverse: function (s) { return s === null || s === undefined ? null : String(s).split('').reverse().join('') },
    left: function (s, n) { return s === null || s === undefined ? null : String(s).slice(0, Math.max(0, n)) },
    right: function (s, n) { return s === null || s === undefined ? null : n <= 0 ? '' : String(s).slice(-n) },
    mid: function (s, p, n) { return s === null || s === undefined ? null : String(s).substr(Math.max(0, p), Math.max(0, n)) },
    substring: function (s, a, b) { if (s === null || s === undefined) return null; s = String(s); if (a < 0) a += s.length; if (b < 0) b += s.length; return s.substring(Math.max(0, a), b === undefined ? s.length : Math.max(0, b)) },
    substringBefore: function (s, sep) { if (empty(s) || sep === null || sep === undefined) return s; var i = String(s).indexOf(String(sep)); return i < 0 ? String(s) : String(s).slice(0, i) },
    substringAfter: function (s, sep) { if (empty(s)) return s; if (sep === null || sep === undefined) return ''; var i = String(s).indexOf(String(sep)); return i < 0 ? '' : String(s).slice(i + String(sep).length) },
    substringBeforeLast: function (s, sep) { if (empty(s) || empty(sep)) return s; var i = String(s).lastIndexOf(String(sep)); return i < 0 ? String(s) : String(s).slice(0, i) },
    substringAfterLast: function (s, sep) { if (empty(s)) return s; if (empty(sep)) return ''; var i = String(s).lastIndexOf(String(sep)); return i < 0 || i === String(s).length - String(sep).length ? '' : String(s).slice(i + String(sep).length) },
    substringBetween: function (s, open, close) { if (s === null || s === undefined) return null; if (close === undefined) close = open; s = String(s); var a = s.indexOf(String(open)); if (a < 0) return null; var b = s.indexOf(String(close), a + String(open).length); return b < 0 ? null : s.slice(a + String(open).length, b) },
    countMatches: function (s, x) { if (empty(s) || empty(x)) return 0; return String(s).split(String(x)).length - 1 },
    isNumeric: function (s) { return !empty(s) && /^\d+$/.test(String(s)) },
    isAlpha: function (s) { return !empty(s) && /^[A-Za-z]+$/.test(String(s)) },
    isAlphanumeric: function (s) { return !empty(s) && /^[A-Za-z0-9]+$/.test(String(s)) },
    chomp: function (s) { return s === null || s === undefined ? null : String(s).replace(/\r?\n$|\r$/, '') },
    abbreviate: function (s, n) { if (s === null || s === undefined) return null; s = String(s); return s.length <= n ? s : s.slice(0, Math.max(0, n - 3)) + '...' },
    length: function (s) { return s === null || s === undefined ? 0 : String(s).length },
    indexOf: function (s, x) { return s === null || s === undefined || x === null || x === undefined ? -1 : String(s).indexOf(String(x)) },
  }
  def(['org.apache.commons.lang3.StringUtils', 'org.apache.commons.lang.StringUtils'], SU)
  def(['org.apache.commons.lang3.StringEscapeUtils', 'org.apache.commons.lang.StringEscapeUtils', 'org.apache.commons.text.StringEscapeUtils'], {
    escapeXml: function (s) { return s === null || s === undefined ? null : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&apos;') },
    escapeXml10: function (s) { return this.escapeXml(s) },
    escapeHtml4: function (s) { return s === null || s === undefined ? null : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;') },
    escapeHtml: function (s) { return this.escapeHtml4(s) },
    unescapeXml: function (s) { return s === null || s === undefined ? null : String(s).replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, '&') },
    unescapeHtml4: function (s) { return this.unescapeXml(s) },
    escapeJava: function (s) { return s === null || s === undefined ? null : JSON.stringify(String(s)).slice(1, -1) },
    escapeJson: function (s) { return s === null || s === undefined ? null : JSON.stringify(String(s)).slice(1, -1) },
  })

  return classes
})
