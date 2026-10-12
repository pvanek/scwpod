package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

func TestSignS3(t *testing.T) {
	// Expected values from botocore's S3SigV4Auth for the same inputs.
	req, _ := http.NewRequest(http.MethodGet, "https://s3.fr-par.scw.cloud/", nil)
	signS3(req, "fr-par", "SCWKEY@p1", "secret", time.Date(2026, 10, 12, 6, 42, 4, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=SCWKEY@p1/20261012/fr-par/s3/aws4_request, " +
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
		"Signature=6a1c02366cb7768b4b6a9314b97e6a85b9f67c62148e3ed81498c79126509348"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20261012T064204Z" {
		t.Errorf("X-Amz-Date = %s", got)
	}
}

func TestBuckets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case strings.Contains(auth, "Credential=AK@p1/") && strings.Contains(auth, "/fr-par/"):
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListAllMyBucketsResult><Buckets>
<Bucket><Name>web-assets</Name><CreationDate>2025-01-02T03:04:05.000Z</CreationDate></Bucket>
<Bucket><Name>backups</Name><CreationDate>2025-02-02T03:04:05.000Z</CreationDate></Bucket>
</Buckets></ListAllMyBucketsResult>`))
		case strings.Contains(auth, "Credential=AK@p2/"):
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>Access Denied.</Message></Error>`))
		default:
			w.Write([]byte(`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`))
		}
	}))
	defer srv.Close()

	s := &Service{accessKey: "AK", secretKey: "SK", httpClient: srv.Client(),
		s3URL: func(scw.Region) string { return srv.URL + "/" }}
	items, err := s.Buckets(context.Background(), []string{"p1", "p2"})
	if len(items) != 2 || items[0].Name != "backups" || items[1].Name != "web-assets" {
		t.Fatalf("items = %+v", items)
	}
	b := items[1]
	if b.Kind != StorageObject || b.ProjectID != "p1" || b.Location != "fr-par" ||
		b.Endpoint != "https://web-assets.s3.fr-par.scw.cloud" || b.CreatedAt.Year() != 2025 {
		t.Errorf("bucket = %+v", b)
	}
	// p2 is denied in all three regions; the list is cut short.
	if err == nil || !strings.Contains(err.Error(), "AccessDenied: Access Denied.") ||
		!strings.Contains(err.Error(), "and 1 more errors") {
		t.Errorf("err = %v", err)
	}
}
