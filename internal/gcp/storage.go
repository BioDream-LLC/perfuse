package gcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Storage is a client for one Cloud Storage bucket.
type Storage struct {
	// Endpoint is https://storage.googleapis.com or an emulator's address.
	Endpoint string
	Bucket   string
	// StorageClass, when set, is given to each object written: STANDARD, NEARLINE, COLDLINE or ARCHIVE.
	StorageClass string
	Creds        *Credentials
	HTTP         *http.Client
}

// NewStorage makes a client. An empty endpoint is Google's.
func NewStorage(endpoint, bucket, storageClass string, creds *Credentials) (*Storage, error) {
	if bucket == "" {
		return nil, errors.New("gcp: cloud storage needs a bucket")
	}
	if endpoint == "" {
		endpoint = "https://storage.googleapis.com"
	}
	return &Storage{Endpoint: strings.TrimRight(endpoint, "/"), Bucket: bucket, StorageClass: storageClass, Creds: creds,
		HTTP: &http.Client{Timeout: 5 * time.Minute}}, nil
}

// Object is one listed object.
type Object struct {
	Name string
	Size int64
}

func (s *Storage) objectURL(name string) string {
	return s.Endpoint + "/storage/v1/b/" + url.PathEscape(s.Bucket) + "/o/" + url.PathEscape(name)
}

// Put writes an object, replacing one of the same name. A multipart upload carries the metadata (content type, storage
// class) with the bytes in one request.
func (s *Storage) Put(ctx context.Context, name string, data []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	meta := map[string]string{"name": name, "contentType": contentType}
	if s.StorageClass != "" {
		meta["storageClass"] = s.StorageClass
	}
	mj, _ := json.Marshal(meta)
	var nonce [12]byte
	_, _ = rand.Read(nonce[:])
	boundary := "perfuse-" + hex.EncodeToString(nonce[:])
	var b bytes.Buffer
	fmt.Fprintf(&b, "--%s\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n%s\r\n--%s\r\nContent-Type: %s\r\n\r\n",
		boundary, mj, boundary, contentType)
	b.Write(data)
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	u := s.Endpoint + "/upload/storage/v1/b/" + url.PathEscape(s.Bucket) + "/o?uploadType=multipart"
	_, err := do(ctx, s.HTTP, s.Creds, http.MethodPost, u, "multipart/related; boundary="+boundary, b.Bytes(), nil)
	return err
}

// List returns the objects under prefix, a page at a time; an empty next token is the last page.
func (s *Storage) List(ctx context.Context, prefix, pageToken string, max int) ([]Object, string, error) {
	q := url.Values{"prefix": {prefix}, "maxResults": {strconv.Itoa(max)}, "fields": {"items(name,size),nextPageToken"}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var out struct {
		Items []struct {
			Name string `json:"name"`
			Size string `json:"size"`
		} `json:"items"`
		Next string `json:"nextPageToken"`
	}
	u := s.Endpoint + "/storage/v1/b/" + url.PathEscape(s.Bucket) + "/o?" + q.Encode()
	if _, err := do(ctx, s.HTTP, s.Creds, http.MethodGet, u, "", nil, &out); err != nil {
		return nil, "", err
	}
	objs := make([]Object, 0, len(out.Items))
	for _, it := range out.Items {
		n, _ := strconv.ParseInt(it.Size, 10, 64)
		objs = append(objs, Object{Name: it.Name, Size: n})
	}
	return objs, out.Next, nil
}

// Get reads an object, refusing one larger than limit.
func (s *Storage) Get(ctx context.Context, name string, limit int64) ([]byte, error) {
	data, err := do(ctx, s.HTTP, s.Creds, http.MethodGet, s.objectURL(name)+"?alt=media", "", nil, nil)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("gcp: object %s is larger than %d bytes", name, limit)
	}
	return data, nil
}

// Delete removes an object. One already gone is not an error.
func (s *Storage) Delete(ctx context.Context, name string) error {
	_, err := do(ctx, s.HTTP, s.Creds, http.MethodDelete, s.objectURL(name), "", nil, nil)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// CreateBucket exists for tests against an emulator.
func (s *Storage) CreateBucket(ctx context.Context, project string) error {
	body, _ := json.Marshal(map[string]string{"name": s.Bucket})
	_, err := do(ctx, s.HTTP, s.Creds, http.MethodPost, s.Endpoint+"/storage/v1/b?project="+url.QueryEscape(project),
		"application/json", body, nil)
	return err
}

func jsonBody(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
