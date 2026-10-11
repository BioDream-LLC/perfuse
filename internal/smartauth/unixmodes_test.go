package smartauth

import "runtime"

// unixModes says whether file modes mean anything here. Windows has none: Go reports 0666 for every writable file, and who
// may read a file is decided by its ACL, inherited from the directory it is in.
var unixModes = runtime.GOOS != "windows"
