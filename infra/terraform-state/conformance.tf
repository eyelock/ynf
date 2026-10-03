# A scratch bucket for the S3 store's conformance suite (internal/store/s3store,
# YNF_S3_BUCKET). It holds the suite to real S3's conditional writes, which MinIO cannot: leases
# need them to be atomic. Nothing here is kept: objects expire after a day.

resource "aws_s3_bucket" "conformance" {
  bucket = "ynf-conformance.eyelock.net"
}

resource "aws_s3_bucket_server_side_encryption_configuration" "conformance" {
  bucket = aws_s3_bucket.conformance.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }

    # AWS sets these on new buckets; declared so the plan does not try to remove them.
    blocked_encryption_types = ["SSE-C"]
    bucket_key_enabled       = false
  }
}

resource "aws_s3_bucket_public_access_block" "conformance" {
  bucket = aws_s3_bucket.conformance.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "conformance" {
  bucket = aws_s3_bucket.conformance.id

  rule {
    id     = "expire-everything"
    status = "Enabled"

    filter {}

    expiration {
      days = 1
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

# The ynf-terraform user can run the suite; it gets this bucket and nothing more.
resource "aws_iam_policy" "conformance_access" {
  name        = "ynf-conformance-access"
  path        = "/ynf/"
  description = "Read and write the S3 store conformance bucket"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListConformanceBucket"
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = [aws_s3_bucket.conformance.arn]
      },
      {
        Sid      = "ReadWriteConformanceObjects"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
        Resource = ["${aws_s3_bucket.conformance.arn}/*"]
      },
    ]
  })
}

resource "aws_iam_group_policy_attachment" "conformance_access" {
  group      = aws_iam_group.terraform.name
  policy_arn = aws_iam_policy.conformance_access.arn
}
