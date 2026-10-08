package config

// Object storage (BR-053). R2 in production, MinIO on a developer's machine:
// both speak S3, so only these values differ. The defaults are MinIO's.
func S3Endpoint() string  { return Getenv("S3_ENDPOINT", "localhost:9000") }
func S3AccessKey() string { return Getenv("S3_ACCESS_KEY", "minioadmin") }
func S3SecretKey() string { return Getenv("S3_SECRET_KEY", "minioadmin") }
func S3Bucket() string    { return Getenv("S3_BUCKET", "new-commerce-dev") }
func S3UseSSL() bool      { return Getenv("S3_USE_SSL", "false") == "true" }

// ImageBaseURL is where public product images are served from: the image
// domain in production (P1-045), the bucket on MinIO locally.
func ImageBaseURL() string {
	return Getenv("IMAGE_BASE_URL", "http://localhost:9000/"+S3Bucket())
}
