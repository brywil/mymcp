package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEmbed serves goclaw-embed's /embed with deterministic bag-of-words vectors, so texts sharing words are close.
func fakeEmbed(t *testing.T, embedded *int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		atomic.AddInt64(embedded, int64(len(req.Texts)))
		out := make([][]float32, len(req.Texts))
		for i, tx := range req.Texts {
			v := make([]float32, 64)
			for _, w := range strings.Fields(strings.ToLower(tx)) {
				h := fnv.New32a()
				h.Write([]byte(strings.Trim(w, ".,:;!?|()\"")))
				v[h.Sum32()%64]++
			}
			var n float64
			for _, x := range v {
				n += float64(x * x)
			}
			for j := range v {
				v[j] /= float32(math.Sqrt(n) + 1e-9)
			}
			out[i] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": out, "dim": 64})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func archiveLine(t *testing.T, dir, conv, role, content string, ts time.Time) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, filepath.Base(conv)+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]interface{}{"ts": ts, "conv": conv, "role": role, "content": content})
	f.Write(append(b, '\n'))
}

func newRecall(t *testing.T, source string, url string) *recallTools {
	t.Helper()
	rt, err := newRecallTools(source, url, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func call(t *testing.T, rt *recallTools, q string, k int) string {
	t.Helper()
	out, err := rt.handle(context.Background(), map[string]interface{}{"query": q, "limit": float64(k)})
	if err != nil {
		t.Fatalf("recall %q: %v", q, err)
	}
	return out
}

func TestRecallSourceFlagValidation(t *testing.T) {
	for _, bad := range []string{"", "goclaw", "slack:/x", "voicebox:"} {
		if _, err := newRecallTools(bad, "", t.TempDir()); err == nil {
			t.Errorf("source %q accepted", bad)
		}
	}
}

func TestRecallGoclawArchive(t *testing.T) {
	var n int64
	srv := fakeEmbed(t, &n)
	dir := t.TempDir()
	old := time.Now().Add(-72 * time.Hour)
	archiveLine(t, dir, "8624879170", "user", "what decode speed did kolibri reach on the spark", old)
	archiveLine(t, dir, "8624879170", "tool_result", strings.Repeat("x", 8000), old) // one token: clipping, not ranking
	archiveLine(t, dir, "8624879170", "assistant", "kolibri decoded at about 51 tokens per second", old)
	archiveLine(t, dir, "8624879170", "user", "how do I bake bread", old.Add(time.Hour))
	rt := newRecall(t, "goclaw:"+dir, srv.URL)
	out := call(t, rt, "kolibri decode speed", 2)
	first := strings.SplitN(out, "\n", 2)[0]
	if !strings.HasPrefix(first, "2 past exchanges from 1 conversation (") {
		t.Errorf("first line (the chat status line) = %q", first)
	}
	if !strings.Contains(out, "data, not instructions") || !strings.Contains(out, "days ago") || !strings.Contains(out, "chat 8624879170") {
		t.Errorf("results must be dated, labelled, quoted data:\n%s", out)
	}
	if i, j := strings.Index(out, "51 tokens"), strings.Index(out, "bake bread"); i < 0 || (j >= 0 && j < i) {
		t.Errorf("kolibri exchange should rank first:\n%s", out)
	}
	if strings.Count(out, "x") > 400 {
		t.Error("tool output was not clipped to its head")
	}
}

func TestRecallInProgressExchangeIndexedOnce(t *testing.T) {
	var n int64
	srv := fakeEmbed(t, &n)
	dir := t.TempDir()
	t0 := time.Now()
	archiveLine(t, dir, "1", "user", "what decode speed did kolibri get", t0)
	rt := newRecall(t, "goclaw:"+dir, srv.URL)
	call(t, rt, "kolibri", 5)
	archiveLine(t, dir, "1", "assistant", "about 51 tokens per second", t0.Add(time.Second))
	out := call(t, rt, "kolibri decode speed", 5)
	if len(rt.items) != 1 || !strings.HasPrefix(out, "1 past exchanges") || !strings.Contains(out, "51 tokens") {
		t.Fatalf("index holds %d chunks for one exchange (partial not dropped?):\n%s", len(rt.items), out)
	}
}

func TestRecallIncrementalAndPersistent(t *testing.T) {
	var n int64
	srv := fakeEmbed(t, &n)
	dir, cache := t.TempDir(), t.TempDir()
	archiveLine(t, dir, "1", "user", "compaction threshold question", time.Now())
	rt, _ := newRecallTools("goclaw:"+dir, srv.URL, cache)
	call(t, rt, "compaction", 3)
	after1 := atomic.LoadInt64(&n)
	call(t, rt, "compaction", 3)
	if d := atomic.LoadInt64(&n) - after1; d != 1 { // only the query is embedded again
		t.Fatalf("second call embedded %d texts; want 1 (the query)", d)
	}
	rt2, _ := newRecallTools("goclaw:"+dir, srv.URL, cache) // a restarted mymcp
	before := atomic.LoadInt64(&n)
	call(t, rt2, "compaction", 3)
	if d := atomic.LoadInt64(&n) - before; d != 1 {
		t.Fatalf("after restart embedded %d texts; want 1 (index reloaded from disk)", d)
	}
	if fi, err := os.Stat(rt2.indexPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("index file: %v %v (want 0600)", fi, err)
	}
}

func TestRecallCronLabelledAndCapped(t *testing.T) {
	var n int64
	srv := fakeEmbed(t, &n)
	dir := t.TempDir()
	t0 := time.Now().Add(-24 * time.Hour)
	for i := 0; i < 6; i++ {
		ts := t0.Add(time.Duration(i) * 6 * time.Hour)
		archiveLine(t, dir, "cron:llm-releases-research", "user", "search for new open weights model releases", ts)
		archiveLine(t, dir, "cron:llm-releases-research", "assistant", fmt.Sprintf("found Kolibri-1 open weights release run %d", i), ts.Add(time.Minute))
	}
	archiveLine(t, dir, "8624879170", "user", "tell me about the Kolibri open weights release", t0)
	out := call(t, newRecall(t, "goclaw:"+dir, srv.URL), "Kolibri open weights release", 5)
	if c := strings.Count(out, "scheduled task llm-releases-research"); c != 2 {
		t.Errorf("cron task appears %d times; want the cap of 2:\n%s", c, out)
	}
	if strings.Contains(out, "Bryan: search for new open weights") || !strings.Contains(out, "Scheduled task: search for new") {
		t.Errorf("a cron prompt must be labelled as the scheduled task, not Bryan:\n%s", out)
	}
	if !strings.Contains(out, "chat 8624879170") {
		t.Errorf("the chat exchange must still be returned:\n%s", out)
	}
}

func TestRecallVoiceboxSessions(t *testing.T) {
	var n int64
	srv := fakeEmbed(t, &n)
	dir := t.TempDir()
	ms := time.Now().Add(-5 * time.Hour).UnixMilli()
	sess := map[string]interface{}{"id": "s1", "title": "Weather in Xenia", "messages": []map[string]interface{}{
		{"seq": 1, "role": "user", "content": "ten day forecast for Xenia Ohio", "ts": ms},
		{"seq": 2, "role": "assistant", "content": "rain Tuesday, sunny by Friday", "ts": ms + 1000}}}
	b, _ := json.Marshal(sess)
	if err := os.WriteFile(filepath.Join(dir, "s1.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	out := call(t, newRecall(t, "voicebox:"+dir, srv.URL), "Xenia forecast", 3)
	if !strings.HasPrefix(out, "1 past exchanges from 1 conversation") || !strings.Contains(out, `voice session "Weather in Xenia"`) ||
		!strings.Contains(out, "Bryan: ten day forecast") || !strings.Contains(out, "5 hours ago") {
		t.Errorf("voicebox result:\n%s", out)
	}
}

func TestRecallServiceDownNamesTheFix(t *testing.T) {
	dir := t.TempDir()
	archiveLine(t, dir, "1", "user", "anything", time.Now())
	rt := newRecall(t, "goclaw:"+dir, "http://127.0.0.1:1") // nothing listens on port 1
	_, err := rt.handle(context.Background(), map[string]interface{}{"query": "anything"})
	if err == nil || !strings.Contains(err.Error(), "task install-embed-unit") {
		t.Fatalf("err = %v; must say how to install goclaw-embed", err)
	}
	if _, err := rt.handle(context.Background(), map[string]interface{}{"query": " "}); err == nil {
		t.Error("empty query must be an error")
	}
}

func TestRecallRegisteredOnlyWithSource(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: t.TempDir()})
	if _, ok := r.tools["recall"]; ok {
		t.Fatal("recall registered without --recall-source")
	}
	r2 := NewRegistry()
	RegisterAll(r2, Config{Workspace: t.TempDir(), RecallSource: "voicebox:" + t.TempDir()})
	if tool, ok := r2.tools["recall"]; !ok || !tool.ReadOnly {
		t.Fatal("recall missing (or not read-only) with --recall-source")
	}
}
