package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

// A started magpie reads the session files' chunks from the file the last
// one wrote, and reads again only the session files that changed since; a
// file of other prices, or a bad one, is read past. Every page is the one
// the ledger gives.
func TestRequestChunksFromDisk(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	least := requestDiskLeast
	requestDiskLeast = 2
	t.Cleanup(func() { requestDiskLeast = least })
	dir := filepath.Join(sessions.CodexDir(), "sessions", "2026", "09", "30")
	writeCodexIdentity(t, "main-a", "native-main", "vscode")
	rollout := func(i int) string {
		return filepath.Join(dir, fmt.Sprintf("rollout-2026-09-30T10-00-0%d-s%d.jsonl", i, i))
	}
	writeCodexCall(t, rollout(0), "main-a", "native-main", `"vscode"`, "resp-0", "m", "low", now.Add(-time.Minute), 60, 6)
	for i := 1; i < 5; i++ {
		at := now.Add(-time.Duration(i) * time.Hour)
		if i%2 == 0 {
			at = at.UTC() // a row's zone is kept as it was read
		}
		writeCodexCall(t, rollout(i), fmt.Sprintf("s%d", i), fmt.Sprintf("native-%d", i), `"cli"`, fmt.Sprintf("resp-%d", i), "m", "high", at, 10*i, i)
	}
	Append(Record{Time: now.Add(-time.Minute), Agent: "codex", Provider: "openai", Model: "m", Input: 60, Output: 6, Status: 200, ResponseID: "resp-0"})

	forget := func() {
		requestDiskSaves.Wait()
		requestCache.Lock()
		requestCache.chunks = map[string]*rowChunk{}
		requestCache.pages, requestCache.parts, requestCache.pairs, requestCache.heats = nil, nil, nil, nil
		requestCache.key = ""
		requestCache.Unlock()
		sessions.Reset()
	}
	var reads atomic.Int32
	page := func() RequestPage {
		t.Helper()
		reads.Store(0)
		got := queryPage(All, Filter{}, 0, 100, func(s sessions.CallSource) []sessions.Call {
			reads.Add(1)
			return sessions.ReadCallSource(s)
		})
		equalPage(t, got, pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
		return got
	}
	first := page()
	// the identity's rollout is a session file too
	all := reads.Load()
	if first.Total != 5 || all != 6 {
		t.Fatalf("first page: %d rows from %d reads, want 5 from 6", first.Total, all)
	}
	requestDiskSaves.Wait()
	if _, err := os.Stat(requestDiskPath()); err != nil {
		t.Fatalf("no file of the chunks: %v", err)
	}

	forget()
	equalPage(t, page(), first)
	if n := reads.Load(); n != 0 {
		t.Fatalf("a start read %d session files, want 0", n)
	}

	// a file written over to the same size is read again for its mtime
	writeCodexCall(t, rollout(1), "s1", "native-1", `"cli"`, "resp-1", "m", "high", now.Add(-time.Hour), 11, 1)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(rollout(1), later, later); err != nil {
		t.Fatal(err)
	}
	forget()
	if p := page(); p.Sum.Input != first.Sum.Input+1 || reads.Load() != 1 {
		t.Fatalf("a changed file: %d input from %d reads, want %d from 1", p.Sum.Input, reads.Load(), first.Sum.Input+1)
	}

	// other prices: the file is of the old ones, and every file is read
	requestDiskSaves.Wait()
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"m":{"id":"m","cost":{"input":20,"output":80,"cache_read":5,"cache_write":25}}}}}`), 0600)
	catalog.Reset()
	forget()
	if p := page(); reads.Load() != all || p.Sum.Cost == first.Sum.Cost {
		t.Fatalf("new prices: %d reads, cost %v (was %v), want %d reads at the new prices", reads.Load(), p.Sum.Cost, first.Sum.Cost, all)
	}

	// a file cut short stands for the entries before the cut
	requestDiskSaves.Wait()
	b, err := os.ReadFile(requestDiskPath())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(requestDiskPath(), b[:len(b)-3], 0600)
	forget()
	if page(); reads.Load() == 0 || reads.Load() == all {
		t.Fatalf("a cut file: %d reads, want the cut entry's and fewer than %d", reads.Load(), all)
	}
	os.WriteFile(requestDiskPath(), []byte("garbage"), 0600)
	forget()
	if page(); reads.Load() != all {
		t.Fatalf("a bad file: %d reads, want %d", reads.Load(), all)
	}
}
