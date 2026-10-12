package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Object Storage has no Scaleway API for buckets, only the S3 protocol,
// which lists the buckets of one project per call. The project is chosen by
// appending "@<project ID>" to the access key.

// bucketRegions are the regions with Object Storage.
var bucketRegions = []scw.Region{scw.RegionFrPar, scw.RegionNlAms, scw.RegionPlWaw}

// s3URL is the Object Storage endpoint of a region.
func s3URL(region scw.Region) string { return "https://s3." + string(region) + ".scw.cloud/" }

// Buckets lists the Object Storage buckets of the given projects in every
// region. Buckets of projects or regions that fail are left out and the
// failures reported in the error.
func (s *Service) Buckets(ctx context.Context, projectIDs []string) ([]StorageItem, error) {
	if s.accessKey == "" || s.secretKey == "" {
		return nil, errors.New("listing buckets: no API key in the configuration")
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  []StorageItem
		errs []error
		seen = map[string]bool{}
	)
	for _, p := range projectIDs {
		for _, r := range bucketRegions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				names, err := s.listBuckets(ctx, p, r)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, fmt.Errorf("listing buckets of project %s in %s: %w", p, r, err))
					return
				}
				for _, b := range names {
					if seen[b.Name] {
						continue
					}
					seen[b.Name] = true
					out = append(out, StorageItem{
						Kind:      StorageObject,
						ID:        b.Name,
						Name:      b.Name,
						ProjectID: p,
						Location:  string(r),
						Endpoint:  "https://" + b.Name + ".s3." + string(r) + ".scw.cloud",
						CreatedAt: b.CreationDate,
					})
				}
			}()
		}
	}
	wg.Wait()
	sortStorage(out)
	return out, summarize(errs)
}

// summarize joins errors, cutting a long list down to the first few.
func summarize(errs []error) error {
	const keep = 2
	if len(errs) <= keep {
		return errors.Join(errs...)
	}
	return errors.Join(append(errs[:keep:keep], fmt.Errorf("and %d more errors", len(errs)-keep))...)
}

type bucket struct {
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
}

type listBucketsResult struct {
	Buckets []bucket `xml:"Buckets>Bucket"`
}

type s3Error struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (s *Service) listBuckets(ctx context.Context, projectID string, region scw.Region) ([]bucket, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.s3URL(region), nil)
	if err != nil {
		return nil, err
	}
	signS3(req, string(region), s.accessKey+"@"+projectID, s.secretKey, time.Now().UTC())
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e s3Error
		if xml.Unmarshal(body, &e) == nil && e.Code != "" {
			return nil, fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	var res listBucketsResult
	if err := xml.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("decoding bucket list: %w", err)
	}
	return res.Buckets, nil
}

// emptySHA256 is the hash of an empty request body.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// signS3 signs a body-less request to the root path with AWS Signature
// Version 4, as Scaleway Object Storage expects.
func signS3(req *http.Request, region, accessKey, secretKey string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)

	const signed = "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		"host:" + req.URL.Host + "\nx-amz-content-sha256:" + emptySHA256 + "\nx-amz-date:" + amzDate + "\n",
		signed,
		emptySHA256,
	}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	hash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(hash[:])

	key := []byte("AWS4" + secretKey)
	for _, part := range []string{day, region, "s3", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signed, sig))
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
