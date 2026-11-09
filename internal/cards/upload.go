package cards

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const cacheControl = "public, max-age=31536000, immutable"

// Uploader puts card PNGs and captain uploads in an R2 bucket under content-addressed keys.
type Uploader struct {
	S3         *s3.Client
	Bucket     string
	PublicBase string
}

// NewUploader points an S3 client at the R2 endpoint (path-style, region auto).
func NewUploader(endpoint, bucket, accessKey, secretKey, publicBase string) *Uploader {
	c := s3.New(s3.Options{
		Region:                     "auto",
		BaseEndpoint:               aws.String(endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Uploader{S3: c, Bucket: bucket, PublicBase: strings.TrimRight(publicBase, "/")}
}

// Put stores png at cards/<sha256>.png and returns its public URL and the hash.
func (u *Uploader) Put(ctx context.Context, png []byte) (url, sha string, err error) {
	return u.put(ctx, "cards", ".png", "image/png", png)
}

// Stored is a card as the launch keeps it: where it is, its hash and size,
// and the copy under Bluesky's limit when the card is over it.
type Stored struct {
	URL   string
	Sha   string
	Bytes int64
	Small string
}

// PutCard stores a rendered card or hero PNG like Put does and, when it is
// over Bluesky's limit, a JPEG copy under it. A card too large to copy is
// stored without one and logged; it is then left out of Bluesky posts.
func (u *Uploader) PutCard(ctx context.Context, png []byte) (Stored, error) {
	url, sha, err := u.Put(ctx, png)
	if err != nil {
		return Stored{}, err
	}
	s := Stored{URL: url, Sha: sha, Bytes: int64(len(png))}
	if len(png) <= BlueskyMaxBytes {
		return s, nil
	}
	c, err := BlueskyCopy(ctx, png)
	if err != nil {
		log.Printf("cards: no Bluesky copy of %s: %v", sha, err)
		return s, nil
	}
	if s.Small, _, err = u.PutImage(ctx, c, "image/jpeg"); err != nil {
		return Stored{}, fmt.Errorf("cards: upload Bluesky copy: %w", err)
	}
	return s, nil
}

// imageExts maps the image types a captain may upload to their stored extension.
var imageExts = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp"}

// PutImage stores a captain's upload as it is, at uploads/<sha256>.<ext> with
// its real content type, and returns its public URL and the hash.
func (u *Uploader) PutImage(ctx context.Context, data []byte, contentType string) (url, sha string, err error) {
	ext, ok := imageExts[contentType]
	if !ok {
		return "", "", fmt.Errorf("cards: unsupported image type %q", contentType)
	}
	return u.put(ctx, "uploads", "."+ext, contentType, data)
}

func (u *Uploader) put(ctx context.Context, dir, ext, contentType string, body []byte) (url, sha string, err error) {
	sum := sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])
	key := dir + "/" + sha + ext
	_, err = u.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(u.Bucket),
		Key:          aws.String(key),
		Body:         bytes.NewReader(body),
		ContentType:  aws.String(contentType),
		CacheControl: aws.String(cacheControl),
	})
	if err != nil {
		return "", "", fmt.Errorf("put %s: %w", key, err)
	}
	return strings.TrimRight(u.PublicBase, "/") + "/" + key, sha, nil
}
