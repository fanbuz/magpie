package davsync

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
)

// folder and file are where the backup lives under the address given.
const (
	folder = "magpie"
	file   = "magpie.magpie-backup"
)

// errChanged is a PUT the server refused because the file changed since it
// was read: another computer synced in between.
var errChanged = errors.New("the file on the server changed meanwhile")

// remote is where the backup is kept: a WebDAV folder, or an S3 bucket.
type remote interface {
	// get reads the backup and its ETag; nil data and no error when there
	// is none yet
	get(ctx context.Context) (data []byte, etag string, err error)
	// put writes it over the version read (etag) — with none, only where
	// there is none yet — and errChanged when another computer wrote in
	// between
	put(ctx context.Context, data []byte, etag string) error
}

// newRemote is c's: an S3 bucket for an s3:// address, WebDAV for the rest.
func newRemote(c Config) (remote, error) {
	if c.S3() {
		return newS3(c)
	}
	return newDAV(c)
}

type dav struct {
	base       *url.URL
	user, pass string
	client     *http.Client
}

func newDAV(c Config) (*dav, error) {
	u, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%q is not a WebDAV address (https://…)", c.URL)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &dav{base: u, user: c.User, pass: c.Password, client: http.DefaultClient}, nil
}

func (d *dav) url(parts ...string) string {
	u := *d.base
	u.Path += "/" + strings.Join(parts, "/")
	return u.String()
}

func (d *dav) do(ctx context.Context, method, u string, body []byte, h map[string]string) (*http.Response, error) {
	res, err := d.send(ctx, method, u, body, h)
	if err == nil && res.StatusCode == http.StatusForbidden {
		res.Body.Close()
		return nil, d.forbidden(ctx)
	}
	return res, err
}

// send is do with a 403 handed back as it is, for the calls that can tell
// what it meant.
func (d *dav) send(ctx context.Context, method, u string, body []byte, h map[string]string) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if d.user != "" || d.pass != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		res.Body.Close()
		return nil, errLogin
	}
	return res, nil
}

var errLogin = errors.New("the WebDAV server refused the user name or password (HTTP 401)")

// forbidden says why a 403 was: servers answer it for a folder that isn't
// there as much as for one the account may not use — a Synology does for a
// shared folder not made — so the folder in the address is looked at first.
func (d *dav) forbidden(ctx context.Context) error {
	dir := d.base.Path
	if dir == "" {
		dir = "/"
	}
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", d.url(), nil)
	if err == nil {
		req.Header.Set("Depth", "0")
		if d.user != "" || d.pass != "" {
			req.SetBasicAuth(d.user, d.pass)
		}
		if res, err := d.client.Do(req); err == nil {
			res.Body.Close()
			switch {
			case res.StatusCode == http.StatusUnauthorized:
				return errLogin
			case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusGone:
				return fmt.Errorf("there is no folder %s on the WebDAV server (HTTP 403): make it there first, or leave it out of the address", dir)
			case res.StatusCode >= 200 && res.StatusCode < 300:
				return fmt.Errorf("the WebDAV server doesn't let this account write in %s (HTTP 403): check the account may change files in that folder", dir)
			}
		}
	}
	return fmt.Errorf("the WebDAV server doesn't let this account use %s (HTTP 403): the folder may not be there, or the account may not have access to it", dir)
}

// there is whether PROPFIND finds u: a server that answers 403 for what
// isn't there may do so for PROPFIND too.
func (d *dav) there(ctx context.Context, u string) bool {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", u, nil)
	if err != nil {
		return true
	}
	req.Header.Set("Depth", "0")
	if d.user != "" || d.pass != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return true
	}
	res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 300
}

// get reads the backup; nil data and no error when there is none yet.
func (d *dav) get(ctx context.Context) (data []byte, etag string, err error) {
	res, err := d.send(ctx, http.MethodGet, d.url(folder, file), nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	// a Synology answers 403, not 404, for a file in a folder that isn't
	// there: before the first sync, magpie's folder isn't
	if res.StatusCode == http.StatusForbidden {
		if !d.there(ctx, d.url(folder)+"/") {
			return nil, "", nil
		}
		return nil, "", d.forbidden(ctx)
	}
	switch {
	// 409: the folder isn't there yet — how 坚果云 (Nutstore) answers a
	// read in it, where others say 404; put makes it
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone || res.StatusCode == http.StatusConflict:
		return nil, "", nil
	case res.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("reading %s from the WebDAV server: HTTP %d", file, res.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	return data, res.Header.Get("ETag"), nil
}

// put writes the backup, only over the version read (etag) when there was
// one, making the folder if the server has none.
func (d *dav) put(ctx context.Context, data []byte, etag string) error {
	h := map[string]string{"Content-Type": "application/octet-stream"}
	if etag != "" {
		h["If-Match"] = etag
	}
	for try := 0; ; try++ {
		res, err := d.send(ctx, http.MethodPut, d.url(folder, file), data, h)
		if err != nil {
			return err
		}
		res.Body.Close()
		switch {
		case res.StatusCode >= 200 && res.StatusCode < 300:
			return nil
		case res.StatusCode == http.StatusPreconditionFailed:
			return errChanged
		case res.StatusCode == http.StatusForbidden && (try > 0 || d.there(ctx, d.url(folder)+"/")):
			return d.forbidden(ctx)
		// a 403 for a folder not there (a Synology) is made as a 404 is
		case (res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict || res.StatusCode == http.StatusForbidden) && try == 0:
			if err := d.mkcol(ctx); err != nil {
				return err
			}
			continue
		case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict:
			return d.nowhere(ctx, res.StatusCode)
		}
		return fmt.Errorf("writing %s to the WebDAV server: HTTP %d", file, res.StatusCode)
	}
}

func (d *dav) mkcol(ctx context.Context) error {
	res, err := d.do(ctx, "MKCOL", d.url(folder)+"/", nil, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	// 405: it is there already; 409: the folder above it isn't
	if res.StatusCode == http.StatusConflict {
		return fmt.Errorf("there is no folder %s on the WebDAV server (HTTP 409): make it there first, or leave it out of the address", d.base.Path)
	}
	if res.StatusCode >= 300 && res.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("making the folder %s on the WebDAV server: HTTP %d", folder, res.StatusCode)
	}
	return nil
}

// nowhere is a folder made, or said to be there, that a file still can't be
// put in: OpenList's and Alist's /dav/ is a list of their storages, where
// MKCOL answers 405 and PUT 404 — the address has to go into one of them.
func (d *dav) nowhere(ctx context.Context, code int) error {
	dir := d.base.Path
	if dir == "" {
		dir = "/"
	}
	subs, status := d.folders(ctx)
	if status == http.StatusNotFound || status == http.StatusConflict || status == http.StatusGone {
		return fmt.Errorf("there is no folder %s on the WebDAV server (HTTP %d): make it there first, or leave it out of the address", dir, code)
	}
	msg := fmt.Sprintf("the WebDAV server has nowhere to put the folder %s in %s (HTTP %d): the address has to be a folder files can be written in", folder, dir, code)
	if len(subs) > 0 {
		u := *d.base
		u.Path = strings.TrimRight(u.Path, "/") + "/" + subs[0]
		msg += fmt.Sprintf(" — on OpenList or Alist, one of its storages (%s), like %s", strings.Join(subs, ", "), u.String())
	}
	return errors.New(msg)
}

// folders are the folders in the address's folder, as PROPFIND lists them,
// and what it answered.
func (d *dav) folders(ctx context.Context) ([]string, int) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", d.url(), nil)
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Depth", "1")
	if d.user != "" || d.pass != "" {
		req.SetBasicAuth(d.user, d.pass)
	}
	res, err := d.client.Do(req)
	if err != nil {
		return nil, 0
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMultiStatus {
		return nil, res.StatusCode
	}
	var ms struct {
		Responses []struct {
			Href       string    `xml:"href"`
			Collection *struct{} `xml:"propstat>prop>resourcetype>collection"`
		} `xml:"response"`
	}
	if xml.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&ms) != nil {
		return nil, res.StatusCode
	}
	self := strings.TrimRight(d.base.Path, "/")
	var out []string
	for _, r := range ms.Responses {
		h := r.Href
		if u, err := url.Parse(h); err == nil {
			h = u.Path
		}
		h = strings.TrimRight(h, "/")
		if r.Collection == nil || h == self || pathpkg.Dir(h) != self && !(self == "" && pathpkg.Dir(h) == "/") {
			continue
		}
		if len(out) < 5 {
			out = append(out, pathpkg.Base(h))
		}
	}
	return out, res.StatusCode
}
