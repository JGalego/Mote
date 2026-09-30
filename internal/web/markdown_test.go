package web

import (
	"strings"
	"testing"
)

func render(t *testing.T, src string) string {
	t.Helper()
	out, err := NewRenderer().Render(src)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderMarkdown(t *testing.T) {
	out := render(t, "# Title\n\nSome *emphasis*, **bold**, `code` and a [link](https://example.com).\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nx := 1\n```\n")
	for _, want := range []string{"<h1", "Title", "<em>emphasis</em>", "<strong>bold</strong>", "<code>code</code>",
		`href="https://example.com"`, "<li>one</li>", "<table>", "<td>1</td>", "<pre>", "x := 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRenderDropsWhatCouldRunScript(t *testing.T) {
	out := render(t, strings.Join([]string{
		"<script>alert(1)</script>",
		"",
		"text <img src=x onerror=alert(2)> more",
		"",
		"[click](javascript:alert(3))",
		"",
		"![pic](javascript:alert(4))",
		"",
		`<iframe src="https://evil.example"></iframe>`,
		"",
		`<audio controls src="x.mp3" onerror="alert(5)"></audio>`,
		"",
		`<video controls src="https://evil.example/v.mp4"></video>`,
		"",
		`<audio controls src="x.mp3"></video>`,
		"",
		`<audio controls src="/etc/passwd"></audio>`,
	}, "\n"))
	for _, bad := range []string{"<script", "onerror", "javascript:", "<iframe", "evil.example", "/etc/passwd", "<audio", "<video"} {
		if strings.Contains(strings.ToLower(out), bad) {
			t.Errorf("output holds %q:\n%s", bad, out)
		}
	}
}

func TestRenderPointsLocalFilesAtTheServer(t *testing.T) {
	out := render(t, "![a photo](my%20photo.png) and ![up](../media/x.gif) and [notes](docs/notes.txt)\n\n![web](https://example.com/a.png) [top](#top) [abs](/etc/passwd)\n")
	for _, want := range []string{
		`src="/file?p=my+photo.png"`,
		`src="/file?p=..%2Fmedia%2Fx.gif"`,
		`href="/file?p=docs%2Fnotes.txt"`,
		`src="https://example.com/a.png"`,
		`href="#top"`,
		`href="/etc/passwd"`, // a path from the root is not the notebook's, and is left as it is
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRenderBuildsPlayersFromEmbeds(t *testing.T) {
	out := render(t, "Before.\n\n<audio controls src=\"meeting.m4a\"></audio>\n\n<video controls src=\"clips/my%20clip.mp4\"></video>\n\nAfter.\n")
	for _, want := range []string{
		`<audio controls preload="metadata" src="/file?p=meeting.m4a"></audio>`,
		`<video controls preload="metadata" src="/file?p=clips%2Fmy+clip.mp4"></video>`,
		"<p>Before.</p>", "<p>After.</p>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "moteembed") {
		t.Errorf("a placeholder was left behind:\n%s", out)
	}
	// Run into other lines it is still a player, not a token on show.
	out = render(t, "Listen:\n<audio controls src=\"a.mp3\"></audio>\nthen read.\n")
	if !strings.Contains(out, `<audio controls preload="metadata" src="/file?p=a.mp3"></audio>`) || strings.Contains(out, "moteembed") {
		t.Errorf("an embed in a paragraph:\n%s", out)
	}
	// In code it is shown, not played.
	out = render(t, "```\n<audio controls src=\"a.mp3\"></audio>\n```\n")
	if strings.Contains(out, "<audio controls preload") || !strings.Contains(out, "&lt;audio") {
		t.Errorf("an embed in code:\n%s", out)
	}
}

func TestRenderIsNotFooledByTheTokenItUses(t *testing.T) {
	// Prose that happens to hold what a placeholder looks like gets nothing
	// swapped in, since the real ones carry a nonce it cannot know.
	out := render(t, "moteembed0000000000000000"+"0x\n\n<audio controls src=\"a.mp3\"></audio>\n")
	if strings.Count(out, "<audio") != 1 || !strings.Contains(out, "moteembed00000000000000000x") {
		t.Errorf("output:\n%s", out)
	}
}

func TestLocal(t *testing.T) {
	for link, want := range map[string]bool{
		"a.png": true, "dir/a.png": true, "../a.png": true, "./a.png": true, "my photo.png": true,
		"": false, "#top": false, "/etc/passwd": false, "\\\\host\\share": false,
		"https://x.example/a.png": false, "http://x": false, "data:text/html,x": false, "mailto:a@b": false, "file:///etc/passwd": false,
	} {
		if got := local(link); got != want {
			t.Errorf("local(%q) = %v, want %v", link, got, want)
		}
	}
}

func TestMedia(t *testing.T) {
	if Media("a.PNG") != "image" || Media("a.m4a") != "audio" || Media("a.mp4") != "video" || Media("a.txt") != "" {
		t.Error("Media does not follow the file's type")
	}
}
