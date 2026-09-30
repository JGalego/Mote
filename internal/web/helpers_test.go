package web

import (
	"net/url"

	"github.com/jgalego/mote/internal/motebook"
)

type urlURL = url.URL

type motebookOutput = motebook.Output

func urlEscape(s string) string { return url.QueryEscape(s) }
