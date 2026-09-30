package motebook

import (
	"path/filepath"
	"strings"
)

var mediaKinds = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".svg": "image",
	".m4a": "audio", ".mp3": "audio", ".wav": "audio", ".ogg": "audio", ".flac": "audio", ".aac": "audio", ".opus": "audio",
	".mp4": "video", ".webm": "video", ".mov": "video", ".m4v": "video",
}

// Kind says what a file is to a reader of a notebook: "image", "audio" or
// "video" if it is one they can be shown, by its extension, else "".
func Kind(path string) string {
	return mediaKinds[strings.ToLower(filepath.Ext(path))]
}
