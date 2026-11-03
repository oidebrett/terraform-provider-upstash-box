package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBox is an in-memory stand-in for the Box API, enough for the client.
type fakeBox struct {
	mu        sync.Mutex
	boxes     map[string]*Box
	polls     int // GETs left before a creating box turns idle
	scripts   map[string]string
	rc        map[string]string
	execs     []string
	resumed   bool
	createReq CreateBoxRequest
}

func newFake(t *testing.T) (*fakeBox, *Client) {
	f := &fakeBox{boxes: map[string]*Box{}, scripts: map[string]string{}, rc: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-key")
	c.PollInterval = time.Millisecond
	return f, c
}

var launchRe = regexp.MustCompile(`echo (\S+) \| base64 -d > (/tmp/\S+)\.sh`)

func (f *fakeBox) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Box-Api-Key") != "test-key" {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"Invalid box API key"}`))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/box"), "/")
	id := ""
	if len(parts) > 1 {
		id = parts[1]
	}
	switch {
	case r.Method == "POST" && id == "":
		json.NewDecoder(r.Body).Decode(&f.createReq)
		b := &Box{ID: "holy-beetle-1", Name: f.createReq.Name, Size: f.createReq.Size, Status: "creating"}
		f.boxes[b.ID] = b
		json.NewEncoder(w).Encode(b)
	case f.boxes[id] == nil:
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"box not found"}`))
	case r.Method == "GET" && len(parts) == 2:
		b := f.boxes[id]
		if b.Status == "creating" {
			if f.polls == 0 {
				b.Status = "idle"
			} else {
				f.polls--
			}
		}
		json.NewEncoder(w).Encode(b)
	case r.Method == "DELETE" && len(parts) == 2:
		delete(f.boxes, id)
		w.Write([]byte(`{"message":"box deleted"}`))
	case r.Method == "POST" && parts[2] == "resume":
		f.resumed = true
		f.boxes[id].Status = "idle"
		w.Write([]byte(`{"message":"box resumed"}`))
	case r.Method == "POST" && parts[2] == "exec":
		var body struct{ Command []string }
		json.NewDecoder(r.Body).Decode(&body)
		cmd := body.Command[2]
		f.execs = append(f.execs, cmd)
		out := ""
		if m := launchRe.FindStringSubmatch(cmd); m != nil {
			s, _ := base64.StdEncoding.DecodeString(m[1])
			f.scripts[m[2]] = string(s)
		} else if strings.HasPrefix(cmd, "cat ") {
			// The script "finishes" on the second poll with the exit code it echoes.
			base := strings.TrimSuffix(strings.Fields(cmd)[1], ".rc")
			if _, seen := f.rc[base]; seen {
				out = strings.TrimSpace(strings.TrimPrefix(f.scripts[base], "exit "))
			}
			f.rc[base] = "polled"
		} else if strings.HasPrefix(cmd, "tail ") {
			out = "install done\n"
		}
		json.NewEncoder(w).Encode(ExecResult{Output: out, ExitCode: 0})
	case parts[2] == "preview" && r.Method == "POST":
		w.Write([]byte(`{"url":"https://holy-beetle-1-18789.preview.box.upstash.com","port":18789,"token":"tok"}`))
	case parts[2] == "preview" && r.Method == "GET":
		w.Write([]byte(`{"previews":[{"id":"holy-beetle-1-18789","port":18789,"url":"https://holy-beetle-1-18789.preview.box.upstash.com"}]}`))
	default:
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"Invalid request body"}`))
	}
}

func TestCreateAndWaitReady(t *testing.T) {
	f, c := newFake(t)
	f.polls = 2
	init := "/home/boxuser/start.sh"
	b, err := c.CreateBox(context.Background(), CreateBoxRequest{Name: "demo", Size: "small", KeepAlive: false, InitCommand: &init})
	if err != nil {
		t.Fatal(err)
	}
	if f.createReq.InitCommand == nil || *f.createReq.InitCommand != init {
		t.Fatalf("init_command not sent: %+v", f.createReq)
	}
	b, err = c.WaitReady(context.Background(), b.ID)
	if err != nil || b.Status != "idle" {
		t.Fatalf("WaitReady = %+v, %v", b, err)
	}
}

func TestKeepAliveOmittedWhenFalse(t *testing.T) {
	raw, _ := json.Marshal(CreateBoxRequest{Size: "small"})
	if strings.Contains(string(raw), "keep_alive") {
		t.Fatalf("keep_alive=false must be omitted (free plan rejects keep_alive): %s", raw)
	}
}

func TestWaitReadyFailsOnErrorStatus(t *testing.T) {
	f, c := newFake(t)
	f.boxes["bad"] = &Box{ID: "bad", Status: "error"}
	if _, err := c.WaitReady(context.Background(), "bad"); err == nil {
		t.Fatal("expected an error for status=error")
	}
}

func TestNotFoundAndDeletedMapToErrNotFound(t *testing.T) {
	f, c := newFake(t)
	if _, err := c.GetBox(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing box: got %v", err)
	}
	f.boxes["gone"] = &Box{ID: "gone", Status: "deleted"}
	if _, err := c.GetBox(context.Background(), "gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted box: got %v", err)
	}
	if err := c.DeleteBox(context.Background(), "nope"); err != nil {
		t.Fatalf("deleting a missing box should succeed, got %v", err)
	}
}

func TestAPIErrorCarriesMessage(t *testing.T) {
	_, c := newFake(t)
	c.APIKey = "wrong"
	_, err := c.GetBox(context.Background(), "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 || apiErr.Message != "Invalid box API key" {
		t.Fatalf("got %v", err)
	}
}

func TestRunDetachedReturnsExitCodeAndLog(t *testing.T) {
	f, c := newFake(t)
	f.boxes["b"] = &Box{ID: "b", Status: "idle"}
	res, err := c.RunDetached(context.Background(), "b", "tfexec-1", "bash", "exit 3", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.LogTail != "install done\n" {
		t.Fatalf("got %+v", res)
	}
	if f.scripts["/tmp/tfexec-1"] != "exit 3" {
		t.Fatalf("script not uploaded intact: %q", f.scripts["/tmp/tfexec-1"])
	}
	// The launcher must not name a process: `pgrep -f`/`pkill -f` inside a box
	// matches the exec shell's own command line and kills it.
	if strings.Contains(f.execs[0], "pgrep") || strings.Contains(f.execs[0], "pkill") {
		t.Fatalf("launcher must not use pgrep/pkill: %s", f.execs[0])
	}
}

func TestRunDetachedTimesOut(t *testing.T) {
	f, c := newFake(t)
	f.boxes["b"] = &Box{ID: "b", Status: "idle"}
	f.rc["/tmp/never"] = "polled"
	f.scripts["/tmp/never"] = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.RunDetached(ctx, "b", "never", "bash", "", time.Millisecond); err == nil {
		t.Fatal("expected a timeout error")
	}
}

func TestEnsureAwakeResumesPausedBox(t *testing.T) {
	f, c := newFake(t)
	f.boxes["p"] = &Box{ID: "p", Status: "paused"}
	if err := c.EnsureAwake(context.Background(), "p"); err != nil || !f.resumed {
		t.Fatalf("resumed=%v err=%v", f.resumed, err)
	}
}

func TestPublicURLs(t *testing.T) {
	f, c := newFake(t)
	f.boxes["holy-beetle-1"] = &Box{ID: "holy-beetle-1", Status: "idle"}
	u, err := c.CreatePublicURL(context.Background(), "holy-beetle-1", 18789, true, false)
	if err != nil || u.Token != "tok" || !strings.HasSuffix(u.URL, ".preview.box.upstash.com") {
		t.Fatalf("got %+v %v", u, err)
	}
	items, err := c.ListPublicURLs(context.Background(), "holy-beetle-1")
	if err != nil || len(items) != 1 || items[0].Port != 18789 {
		t.Fatalf("got %+v %v", items, err)
	}
}
