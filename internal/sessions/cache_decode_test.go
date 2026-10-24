package sessions

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// The kept file decoded on a few goroutines is what one json.Unmarshal of
// it was, and is refused where that was.
func TestDecodeCacheIsUnmarshal(t *testing.T) {
	unmarshal := func(b []byte, zone string) (map[string]*state, bool) {
		var c cacheFile
		if json.Unmarshal(b, &c) != nil || c.Version != cacheVersion || c.Zone != zone || c.Files == nil {
			return nil, false
		}
		return c.Files, true
	}
	var many strings.Builder
	fmt.Fprintf(&many, `{"version":%d,"zone":"z","files":{`, cacheVersion)
	for i := range 3000 {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `"/s/%d.jsonl":{"size":%d,"mod":%d,"title":"té %d","models":{"m":{"input":%d}},"claude":[{"key":"message:%d","m":{"request":"r%d"}}],"days":{"2026-10-%02d":{"models":{"m":{"output":%d}}}}}`, i, i, i*7, i, i, i, i, 1+i%28, i)
	}
	many.WriteString(`}}`)
	v := cacheVersion
	cases := map[string]string{
		"many":         many.String(),
		"escaped keys": fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a\"b\\cé.jsonl":{"size":1},"/n":null}}`, v),
		"twice":        fmt.Sprintf(`{"zone":"z","files":{"/a":{"size":1},"/a":{"size":2}},"version":%d}`, v),
		"empty":        fmt.Sprintf(`{"version":%d,"zone":"z","files":{}}`, v),
		"no files":     fmt.Sprintf(`{"version":%d,"zone":"z","files":null}`, v),
		"old version":  fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a":{"size":1}}}`, v-1),
		"other zone":   fmt.Sprintf(`{"version":%d,"zone":"y","files":{"/a":{"size":1}}}`, v),
		"bad file":     fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a":{"size":"one"}}}`, v),
		"bad late":     strings.Replace(many.String(), `"size":2999`, `"size":"x"`, 1),
		"cut":          many.String()[:many.Len()-5],
		"trailing":     many.String() + " x",
		"no comma":     fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a":{"size":1} "/b":{"size":2}}}`, v),
		"garbage":      "garbage",
		"empty bytes":  "",
	}
	for name, in := range cases {
		want, wantOK := unmarshal([]byte(in), "z")
		got, ok := decodeCache([]byte(in), "z")
		if ok != wantOK || !reflect.DeepEqual(got, want) {
			wj, _ := json.Marshal(want)
			gj, _ := json.Marshal(got)
			t.Errorf("%s: got %v %.300s, want %v %.300s", name, ok, gj, wantOK, wj)
		}
	}
}

// A title, cwd or model that many kept files repeat is held once, not
// once a file: 23.7k sessions a script ran with one prompt held 11 MB of
// copies of it.
func TestDecodeCacheSharesRepeats(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":%d,"zone":"z","files":{`, cacheVersion)
	for i := range 2000 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"/s/%d.jsonl":{"size":%d,"title":"重要执行方式 %d","cwd":"/w/p","model":"gpt-5.5","codex_identity":{"id":"c%d","role":"subagent"}}`, i, i, i%2, i)
	}
	b.WriteString(`}}`)
	files, ok := decodeCache([]byte(b.String()), "z")
	if !ok || len(files) != 2000 {
		t.Fatalf("decoded %d files, %v", len(files), ok)
	}
	held := map[string]map[*byte]bool{}
	for _, st := range files {
		for name, s := range map[string]string{"title": st.Title, "cwd": st.Cwd, "model": st.Model, "role": st.CodexIdentity.Role} {
			if held[name] == nil {
				held[name] = map[*byte]bool{}
			}
			held[name][unsafe.StringData(s)] = true
		}
	}
	for name, want := range map[string]int{"title": 2, "cwd": 1, "model": 1, "role": 1} {
		if len(held[name]) != want {
			t.Errorf("%s: %d copies held, want %d", name, len(held[name]), want)
		}
	}
	if st := files["/s/3.jsonl"]; st.Title != "重要执行方式 1" || st.CodexIdentity.ID != "c3" {
		t.Fatalf("file 3: %+v", st)
	}
}
