package ui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// The README preview is a two-process affair, mirroring TestRun: only a
// real g_application_run main loop maps genuine windows, so a child
// process runs the app with fixture data while the parent captures each
// window by its X11 id and layers them into docs/preview.png.
//
//	BTCX_PREVIEW=1 GDK_BACKEND=x11 go test -run TestPreviewScreenshot ./internal/ui
//
// scripts/preview.sh wraps this. The session needs an X display (XWayland
// on Wayland desktops): that is where `ffmpeg -f x11grab` reads the
// windows.
const (
	previewChildEnv = "BTCX_PREVIEW_CHILD"
	previewOutEnv   = "BTCX_PREVIEW_OUT"
	previewXIDsEnv  = "BTCX_PREVIEW_XIDS"
	previewQuitMs   = 9_000 // child lifetime; the parent captures well before it
)

// TestPreviewScreenshot spawns the child (which shows the scene), then
// captures and layers the two windows.
func TestPreviewScreenshot(t *testing.T) {
	if os.Getenv("BTCX_PREVIEW") != "1" {
		t.Skip("set BTCX_PREVIEW=1 (run scripts/preview.sh)")
	}
	for _, tool := range []string{"ffmpeg", "magick"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("capture tool %q not found", tool)
		}
	}
	if os.Getenv("DISPLAY") == "" {
		t.Skip("preview capture needs an X display (DISPLAY)")
	}
	if !strings.Contains(os.Getenv("GDK_BACKEND"), "x11") {
		t.Skip("GDK_BACKEND must include x11; run scripts/preview.sh")
	}
	out := os.Getenv(previewOutEnv)
	if out == "" {
		out = "docs/preview.png"
	}
	out, err := filepath.Abs(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	xidsPath := out + ".xids"
	_ = os.Remove(xidsPath)

	child := exec.Command(os.Args[0], "-test.run=^TestPreviewChild$", "-test.timeout=90s")
	child.Env = append(os.Environ(),
		previewChildEnv+"=1",
		previewOutEnv+"="+out,
		previewXIDsEnv+"="+xidsPath,
		fmt.Sprintf("BTCX_PREVIEW_CHILD_MS=%d", previewQuitMs),
	)
	var childLog bytes.Buffer
	child.Stdout = &childLog
	child.Stderr = &childLog
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	killed := false
	kill := func() {
		if !killed {
			_ = child.Process.Kill()
			_ = child.Wait()
			killed = true
		}
	}

	// Wait for the child to report both window ids, then let the WebKit
	// web process finish rendering before grabbing pixels.
	mainID, composeID := "", ""
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(xidsPath); err == nil {
			if parts := strings.Fields(string(b)); len(parts) == 2 {
				mainID, composeID = parts[0], parts[1]
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if mainID == "" {
		kill()
		t.Fatalf("child never reported window ids\nchild log:\n%s", childLog.String())
	}
	time.Sleep(1200 * time.Millisecond)

	work := out + ".work"
	if err := os.MkdirAll(work, 0o755); err != nil {
		kill()
		t.Fatal(err)
	}
	if os.Getenv("BTCX_PREVIEW_KEEP") == "" {
		defer os.RemoveAll(work)
	} else {
		t.Logf("keeping work dir %s", work)
	}

	grab := func(id, path string) error {
		cmd := exec.Command("ffmpeg", "-loglevel", "error",
			"-f", "x11grab", "-window_id", id, "-draw_mouse", "0",
			"-i", os.Getenv("DISPLAY"), "-frames:v", "1", "-y", path)
		if outp, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg window %s: %v: %s", id, err, outp)
		}
		return nil
	}
	if err := grab(mainID, filepath.Join(work, "main.png")); err != nil {
		kill()
		t.Fatalf("capture: %v\nchild log:\n%s", err, childLog.String())
	}
	if err := grab(composeID, filepath.Join(work, "compose.png")); err != nil {
		kill()
		t.Fatalf("capture: %v\nchild log:\n%s", err, childLog.String())
	}
	if err := layerPreview(work, out); err != nil {
		kill()
		t.Fatalf("composite: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		kill()
		t.Fatalf("preview not written: %v", err)
	}
	if info.Size() < 30_000 {
		kill()
		t.Fatalf("preview suspiciously small: %d bytes\nchild log:\n%s",
			info.Size(), childLog.String())
	}
	_ = os.Remove(xidsPath)

	if !killed {
		if err := child.Wait(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, childLog.String())
		}
		killed = true
	}
	t.Logf("wrote %s (%d bytes)", out, info.Size())
}

// layerPreview composes the two window captures onto a calm dark canvas:
// the mail window at the top left, the compose window overlapping it to
// the lower right, both with soft drop shadows.
func layerPreview(work, out string) error {
	mainPath := filepath.Join(work, "main.png")
	composePath := filepath.Join(work, "compose.png")
	mw, err := magickSize(mainPath, "%w")
	if err != nil {
		return err
	}
	mh, err := magickSize(mainPath, "%h")
	if err != nil {
		return err
	}
	cw, err := magickSize(composePath, "%w")
	if err != nil {
		return err
	}
	ch, err := magickSize(composePath, "%h")
	if err != nil {
		return err
	}

	const margin = 64
	offX := margin + mw*46/100
	offY := margin + mh*38/100
	canvasW := max(margin+mw+margin, offX+cw+margin)
	canvasH := max(margin+mh+margin, offY+ch+margin)

	run := func(args ...string) error {
		cmd := exec.Command("magick", args...)
		if outp, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("magick %v: %v: %s", args, err, outp)
		}
		return nil
	}
	shadow := func(src, dst, spec string) error {
		return run(src, "-alpha", "set",
			"(", "+clone", "-background", "black", "-shadow", spec, ")",
			"-background", "none", "-layers", "merge", "+repage", dst)
	}

	// Tight drop shadows: a small blur close to the window edge reads as
	// a shadow; a wide one turns into a grey fog across light mail bodies
	// (and looks inverted there). The source windows are opaque, so give
	// the shadow an explicit alpha to blur before layering it underneath.
	mainS := filepath.Join(work, "main_s.png")
	if err := shadow(mainPath, mainS, "30x7+0+9"); err != nil {
		return err
	}
	composeS := filepath.Join(work, "compose_s.png")
	if err := shadow(composePath, composeS, "35x9+0+11"); err != nil {
		return err
	}
	// Transparent canvas: the preview must sit on any page background,
	// so nothing is drawn behind the windows but the drop shadows.
	canvas := filepath.Join(work, "canvas.png")
	if err := run("-size", fmt.Sprintf("%dx%d", canvasW, canvasH),
		"xc:none", canvas); err != nil {
		return err
	}
	return run(canvas, mainS,
		"-geometry", fmt.Sprintf("+%d+%d", margin, margin), "-compose", "over", "-composite",
		composeS,
		"-geometry", fmt.Sprintf("+%d+%d", offX, offY), "-compose", "over", "-composite",
		out)
}

// magickSize reads one dimension of an image.
func magickSize(path, format string) (int, error) {
	out, err := exec.Command("magick", "identify", "-format", format, path).Output()
	if err != nil {
		return 0, fmt.Errorf("identify %s: %w", path, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("identify %s: %q", path, out)
	}
	return n, nil
}

// TestPreviewChild builds and runs the real application with the preview
// fixtures; the parent captures while it is alive.
func TestPreviewChild(t *testing.T) {
	if os.Getenv(previewChildEnv) == "" {
		t.Skip("only runs as a subprocess of TestPreviewScreenshot")
	}
	requireGTK(t)
	quitAfter := 10_000
	if v := os.Getenv("BTCX_PREVIEW_CHILD_MS"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &quitAfter); n != 1 || err != nil {
			t.Fatalf("bad BTCX_PREVIEW_CHILD_MS: %q", v)
		}
	}
	ctrl := &previewController{quitAfter: quitAfter}
	appID := fmt.Sprintf("org.btcx.Preview%d", os.Getpid())
	if code := Run(Options{
		Title:   "btcx",
		Version: "preview",
		Theme:   "dark",
		AppID:   appID,
	}, ctrl, []string{"btcx"}); code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
}

// previewController delivers the fixture scene on Start, reports the
// window ids once both windows are mapped, and quits after quitAfter
// milliseconds.
type previewController struct {
	stubController
	quitAfter int
	events    contract.Events
}

func (c *previewController) SetEvents(events contract.Events) { c.events = events }

func (c *previewController) Start() error {
	// The window exists by now but is not yet presented, so the default
	// size still applies: a slightly shorter window keeps the preview
	// compact.
	if a, ok := c.events.(*app); ok && a.win != nil {
		a.win.SetDefaultSize(windowWidth, 640)
	}
	e := c.events
	go func() {
		// Delivered from another goroutine on purpose: the UI marshals.
		e.OnFolders("work", previewFolders())
		e.OnMessages("work", "INBOX", previewRows())
		e.OnMessageOpened(previewMessage())
		// The draft handler opens the compose window over the reader.
		e.OnDraftReady(1, previewDraft())
	}()
	c.scheduleXIDs()
	if app := gio.ApplicationGetDefault(); app != nil {
		glib.TimeoutAdd(uint(c.quitAfter), func() bool {
			app.Quit()
			return false
		})
	}
	return nil
}

// scheduleXIDs polls until both windows exist and are realized, then
// writes their X11 ids for the parent process to capture.
func (c *previewController) scheduleXIDs() {
	path := os.Getenv(previewXIDsEnv)
	if path == "" {
		return
	}
	attempts := 0
	var tick func()
	tick = func() {
		attempts++
		a, ok := c.events.(*app)
		if ok && a != nil && a.win != nil {
			if mainXID := xidOf(&a.win.Window); mainXID != 0 {
				var composeXID uintptr
				for cw := range a.composes {
					if x := xidOf(&cw.Window); x != 0 {
						composeXID = x
						break
					}
				}
				if composeXID != 0 {
					_ = os.WriteFile(path,
						[]byte(fmt.Sprintf("%d %d\n", mainXID, composeXID)), 0o644)
					return
				}
			}
		}
		if attempts < 80 {
			glib.TimeoutAdd(250, func() bool {
				tick()
				return false
			})
		}
	}
	glib.TimeoutAdd(500, func() bool {
		tick()
		return false
	})
}

func previewFolders() []contract.FolderInfo {
	return []contract.FolderInfo{
		{Name: "INBOX", Total: 42, Unread: 3},
		{Name: "Sent", Role: "sent"},
		{Name: "Drafts", Role: "drafts"},
		{Name: "Archive", Role: "archive"},
	}
}

func previewRows() []contract.MessageRow {
	day := func(d, h int) time.Time { return time.Date(2026, time.November, d, h, 24, 0, 0, time.Local) }
	return []contract.MessageRow{
		{Ref: model.Ref{Mailbox: "INBOX", UID: 41}, Subject: "November's deep-sky picks",
			From: model.Address{Name: "Astronomy Weekly", Addr: "dispatch@sky.example"},
			Date: day(6, 8), Unread: true},
		{Ref: model.Ref{Mailbox: "INBOX", UID: 40}, Subject: "Re: telescope shortlist",
			From: model.Address{Name: "Jane Doe", Addr: "jane@example.org"},
			Date: day(5, 17)},
		{Ref: model.Ref{Mailbox: "INBOX", UID: 39}, Subject: "Pipeline #482 passed",
			From: model.Address{Name: "Buildkite", Addr: "no-reply@buildkite.example"},
			Date: day(5, 9), Unread: true},
		{Ref: model.Ref{Mailbox: "INBOX", UID: 38}, Subject: "Your two-factor recovery codes",
			From: model.Address{Name: "Fastmail", Addr: "support@fastmail.example"},
			Date: day(4, 14)},
		{Ref: model.Ref{Mailbox: "INBOX", UID: 37}, Subject: "Sunday lunch",
			From: model.Address{Name: "Mum", Addr: "elaine@example.net"},
			Date: day(3, 19)},
		{Ref: model.Ref{Mailbox: "INBOX", UID: 36}, Subject: "Invoice 2026-11",
			From: model.Address{Name: "Posteo", Addr: "support@posteo.example"},
			Date: day(2, 11), HasAttachments: true},
		{Ref: model.Ref{Mailbox: "INBOX", UID: 35}, Subject: "[users] Announcing rustfmt 2.0",
			From: model.Address{Name: "Rust Users List", Addr: "users@list.rust-lang.example"},
			Date: day(1, 8), Flagged: true},
	}
}

// previewMessage is the opened HTML mail: the body mirrors what
// render.WebHTML produces (sanitized subset, own stylesheet).
func previewMessage() contract.MessageView {
	const body = `<!doctype html>
<html><head><meta charset="utf-8"><style>body{font-family:sans-serif;margin:10px;}pre{white-space:pre-wrap;}</style></head><body>
<p>Hi Jane,</p>
<p>The New Moon on the 10th lands right in the middle of the week, so
get away from the city lights while the sky is at its darkest. These are
the three targets I would put on the list first:</p>
<h3>Targets for this week</h3>
<ul>
<li><strong>M42</strong>, the Orion Nebula, best after 21:00</li>
<li><strong>M31</strong>, Andromeda, low in the west until midnight</li>
<li><strong>NGC 869/884</strong>, the Double Cluster, high all night</li>
</ul>
<blockquote>The best evenings are the ones where the forecast is only a
suggestion.</blockquote>
<p>Full predictions and a printable finder chart are on
<a href="https://sky.example/november">the website</a>. Bring a red
torch and a flask.</p>
<h3>Field notes from last month</h3>
<p>The Orion Nebula rewards patience: give your eyes twenty minutes of
darkness before you judge what you can see. Averted vision works better
than looking straight at it, and a low-power eyepiece often shows more
than a high-power one.</p>
<p>If the haze holds, the Double Cluster is the consolation prize. It
fits happily in binoculars and never disappoints, even from the edge of
town.</p>
<h3>Calendar</h3>
<ul>
<li>10 Nov: New Moon, 19:02 UTC</li>
<li>17 Nov: Geminids peak before dawn</li>
<li>24 Nov: Last Quarter, 06:12 UTC</li>
</ul>
<p>Clear skies,<br>Elena</p>
</body></html>`
	return contract.MessageView{
		Account:  "work",
		Ref:      model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 41},
		Subject:  "November's deep-sky picks",
		From:     []model.Address{{Name: "Astronomy Weekly", Addr: "dispatch@sky.example"}},
		To:       []model.Address{{Name: "Jane Doe", Addr: "jane@example.org"}},
		Cc:       []model.Address{{Name: "Elena Varga", Addr: "elena@sky.example"}},
		Date:     time.Date(2026, time.November, 6, 8, 24, 0, 0, time.Local),
		HTMLBody: body,
		Attachments: []contract.AttachmentInfo{
			{Index: 0, Filename: "orion-observing-notes.pdf", ContentType: "application/pdf", Size: 24_576},
		},
	}
}

func previewDraft() contract.ComposeDraft {
	return contract.ComposeDraft{
		Account: "work",
		To:      []model.Address{{Name: "Elena Varga", Addr: "elena@sky.example"}},
		Cc:      []model.Address{{Name: "Jane Doe", Addr: "jane@example.org"}},
		Subject: "Re: November's deep-sky picks",
		Plain: "Hi Elena,\n\n" +
			"The Double Cluster advice was spot on. I could see it clearly\n" +
			"even from the park, and M42 was a real surprise at 40x.\n\n" +
			"I'll bring the red torch on Thursday.\n\n" +
			"Jane\n",
	}
}
