# One hosted Perfuse in one AWS Region, built so that its data stays there.
#
# Everything is in var.region: the server, its PostgreSQL database and that database's automated backups, the bucket
# Perfuse's own backups go to, the server's logs, the record of every support session, and the keys all of them are
# encrypted with. Nothing is replicated to another Region (S3 has no replication rule, RDS no cross-Region backup copy),
# support reaches the server through Session Manager in the Region (no SSH, no bastion), and Perfuse itself runs with
# -residency, so a channel cannot be pointed at a bucket or queue elsewhere.
#
# What this does not decide: where the hosted organisation's own systems and partners are. Those are the hosts in the
# record of processing (Data protection screen), for the controller to check.

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      "perfuse:customer"  = var.customer
      "perfuse:residency" = var.residency
      "managed-by"        = "opentofu"
    }
  }
}

data "aws_caller_identity" "me" {}
data "aws_partition" "p" {}

data "aws_availability_zones" "here" {
  state = "available"
}

locals {
  name          = "perfuse-${var.customer}"
  azs           = slice(data.aws_availability_zones.here.names, 0, 2)
  ingress_cidrs = length(var.allowed_cidrs) > 0 ? var.allowed_cidrs : ["0.0.0.0/0"]
}

# ---- keys -------------------------------------------------------------------------------------------------------------

resource "aws_kms_key" "data" {
  description             = "${local.name}: disks, database, backups, logs and secrets, in ${var.region}"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  multi_region            = false
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "Account"
        Effect    = "Allow"
        Principal = { AWS = "arn:${data.aws_partition.p.partition}:iam::${data.aws_caller_identity.me.account_id}:root" }
        Action    = "kms:*"
        Resource  = "*"
      },
      {
        Sid       = "Logs"
        Effect    = "Allow"
        Principal = { Service = "logs.${var.region}.amazonaws.com" }
        Action    = ["kms:Encrypt*", "kms:Decrypt*", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
        Resource  = "*"
        Condition = { ArnLike = { "kms:EncryptionContext:aws:logs:arn" = "arn:${data.aws_partition.p.partition}:logs:${var.region}:${data.aws_caller_identity.me.account_id}:*" } }
      },
    ]
  })
}

resource "aws_kms_alias" "data" {
  name          = "alias/${local.name}"
  target_key_id = aws_kms_key.data.key_id
}

# ---- network ----------------------------------------------------------------------------------------------------------

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = local.name }
}

resource "aws_subnet" "public" {
  count             = 2
  vpc_id            = aws_vpc.main.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, count.index)
  tags              = { Name = "${local.name}-public-${count.index}" }
}

resource "aws_subnet" "private" {
  count             = 2
  vpc_id            = aws_vpc.main.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, 10 + count.index)
  tags              = { Name = "${local.name}-private-${count.index}" }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  count          = 2
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_eip" "nat" {
  count  = var.nat_gateway ? 1 : 0
  domain = "vpc"
}

resource "aws_nat_gateway" "main" {
  count         = var.nat_gateway ? 1 : 0
  allocation_id = aws_eip.nat[0].id
  subnet_id     = aws_subnet.public[0].id
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id
  dynamic "route" {
    for_each = var.nat_gateway ? [1] : []
    content {
      cidr_block     = "0.0.0.0/0"
      nat_gateway_id = aws_nat_gateway.main[0].id
    }
  }
}

resource "aws_route_table_association" "private" {
  count          = 2
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

# AWS services in the Region without the internet: S3 (free gateway endpoint) and what Session Manager, CloudWatch Logs
# and Secrets Manager need, so support access and logs never leave the Region's network.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.private.id]
}

resource "aws_security_group" "endpoints" {
  name        = "${local.name}-endpoints"
  description = "AWS service endpoints, reached from the server"
  vpc_id      = aws_vpc.main.id
  ingress {
    description     = "HTTPS from the server"
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    security_groups = [aws_security_group.server.id]
  }
}

resource "aws_vpc_endpoint" "interface" {
  for_each          = var.private_endpoints ? toset(["ssm", "ssmmessages", "ec2messages", "logs", "secretsmanager"]) : toset([])
  vpc_id            = aws_vpc.main.id
  service_name      = "com.amazonaws.${var.region}.${each.key}"
  vpc_endpoint_type = "Interface"
  # In the server's Availability Zone only: each endpoint is charged per zone, and the server is in this one.
  subnet_ids          = [aws_subnet.private[0].id]
  security_group_ids  = [aws_security_group.endpoints.id]
  private_dns_enabled = true
}

# ---- security groups --------------------------------------------------------------------------------------------------

resource "aws_security_group" "server" {
  name        = "${local.name}-server"
  description = "The Perfuse server"
  vpc_id      = aws_vpc.main.id
  egress {
    description = "Out: partners, AWS services, the release download"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_vpc_security_group_ingress_rule" "server" {
  for_each          = { for pair in setproduct([for p in concat([8443], var.mllp_ports) : tostring(p)], local.ingress_cidrs) : "${pair[0]}/${pair[1]}" => pair }
  security_group_id = aws_security_group.server.id
  description       = "From the load balancer, which passes client addresses through"
  cidr_ipv4         = each.value[1]
  ip_protocol       = "tcp"
  from_port         = tonumber(each.value[0])
  to_port           = tonumber(each.value[0])
}

resource "aws_vpc_security_group_ingress_rule" "health" {
  for_each          = toset([for p in concat([8443], var.mllp_ports) : tostring(p)])
  security_group_id = aws_security_group.server.id
  description       = "Health checks from the load balancer subnets"
  cidr_ipv4         = var.vpc_cidr
  ip_protocol       = "tcp"
  from_port         = tonumber(each.key)
  to_port           = tonumber(each.key)
}

resource "aws_security_group" "db" {
  name        = "${local.name}-db"
  description = "PostgreSQL, from the server only"
  vpc_id      = aws_vpc.main.id
  ingress {
    description     = "PostgreSQL from the server"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.server.id]
  }
}

# ---- database ---------------------------------------------------------------------------------------------------------

resource "aws_db_subnet_group" "main" {
  name       = local.name
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_db_instance" "main" {
  identifier                          = local.name
  engine                              = "postgres"
  engine_version                      = "16"
  instance_class                      = var.db_instance_class
  allocated_storage                   = 50
  max_allocated_storage               = 500
  storage_type                        = "gp3"
  storage_encrypted                   = true
  kms_key_id                          = aws_kms_key.data.arn
  db_name                             = "perfuse"
  username                            = "perfuse"
  manage_master_user_password         = true
  master_user_secret_kms_key_id       = aws_kms_key.data.arn
  db_subnet_group_name                = aws_db_subnet_group.main.name
  vpc_security_group_ids              = [aws_security_group.db.id]
  multi_az                            = var.db_multi_az
  backup_retention_period             = var.db_backup_days
  copy_tags_to_snapshot               = true
  deletion_protection                 = true
  skip_final_snapshot                 = false
  final_snapshot_identifier           = "${local.name}-final"
  publicly_accessible                 = false
  iam_database_authentication_enabled = false
  performance_insights_enabled        = true
  performance_insights_kms_key_id     = aws_kms_key.data.arn
  auto_minor_version_upgrade          = true
  # Deliberately no aws_db_instance_automated_backups_replication: backups stay in var.region.
}

# ---- Perfuse's own backups --------------------------------------------------------------------------------------------

resource "aws_s3_bucket" "backups" {
  bucket = "${local.name}-backups-${data.aws_caller_identity.me.account_id}-${var.region}"
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.data.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    id     = "expire"
    status = "Enabled"
    filter {}
    expiration {
      days = var.backup_days
    }
    noncurrent_version_expiration {
      noncurrent_days = var.backup_days
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 2
    }
  }
}

resource "aws_s3_bucket_policy" "backups" {
  bucket = aws_s3_bucket.backups.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "TLSOnly"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.backups.arn, "${aws_s3_bucket.backups.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}

# ---- logs and support sessions ----------------------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "server" {
  name              = "/perfuse/${var.customer}/server"
  retention_in_days = var.log_days
  kms_key_id        = aws_kms_key.data.arn
}

resource "aws_cloudwatch_log_group" "sessions" {
  name              = "/perfuse/${var.customer}/support-sessions"
  retention_in_days = var.log_days
  kms_key_id        = aws_kms_key.data.arn
}

# Every support session is recorded, encrypted, in this Region. The document name is the account's Session Manager
# preferences; a second hosted customer in the same account and Region shares them.
resource "aws_ssm_document" "session_prefs" {
  name            = "SSM-SessionManagerRunShell"
  document_type   = "Session"
  document_format = "JSON"
  content = jsonencode({
    schemaVersion = "1.0"
    description   = "Session Manager: sessions logged in ${var.region}"
    sessionType   = "Standard_Stream"
    inputs = {
      cloudWatchLogGroupName      = aws_cloudwatch_log_group.sessions.name
      cloudWatchEncryptionEnabled = true
      cloudWatchStreamingEnabled  = true
      kmsKeyId                    = aws_kms_key.data.arn
      runAsEnabled                = false
      idleSessionTimeout          = "20"
    }
  })
}

# ---- the server -------------------------------------------------------------------------------------------------------

data "aws_ssm_parameter" "al2023" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_iam_role" "server" {
  name = "${local.name}-server"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.server.name
  policy_arn = "arn:${data.aws_partition.p.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "server" {
  name = "perfuse"
  role = aws_iam_role.server.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "Backups", Effect = "Allow", Action = ["s3:PutObject", "s3:GetObject", "s3:ListBucket"],
      Resource = [aws_s3_bucket.backups.arn, "${aws_s3_bucket.backups.arn}/*"] },
      { Sid = "DatabaseSecret", Effect = "Allow", Action = ["secretsmanager:GetSecretValue"],
      Resource = [aws_db_instance.main.master_user_secret[0].secret_arn] },
      { Sid = "Key", Effect = "Allow", Action = ["kms:Decrypt", "kms:GenerateDataKey"], Resource = [aws_kms_key.data.arn] },
      { Sid = "Logs", Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"],
      Resource = ["${aws_cloudwatch_log_group.server.arn}:*", "${aws_cloudwatch_log_group.sessions.arn}:*"] },
    ]
  })
}

resource "aws_iam_instance_profile" "server" {
  name = "${local.name}-server"
  role = aws_iam_role.server.name
}

resource "aws_instance" "server" {
  ami                    = data.aws_ssm_parameter.al2023.value
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.private[0].id
  vpc_security_group_ids = [aws_security_group.server.id]
  iam_instance_profile   = aws_iam_instance_profile.server.name
  monitoring             = true
  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  root_block_device {
    volume_type = "gp3"
    volume_size = 30
    encrypted   = true
    kms_key_id  = aws_kms_key.data.arn
  }
  user_data_replace_on_change = false
  user_data = templatefile("${path.module}/setup.sh.tftpl", {
    region     = var.region
    version    = var.perfuse_version
    domain     = var.domain
    residency  = var.residency
    secret_arn = aws_db_instance.main.master_user_secret[0].secret_arn
    db_host    = aws_db_instance.main.address
    bucket     = aws_s3_bucket.backups.bucket
    log_group  = aws_cloudwatch_log_group.server.name
    mllp_ports = join(" ", [for p in var.mllp_ports : tostring(p)])
  })
  tags = { Name = local.name }
  lifecycle {
    ignore_changes = [ami]
  }
}

# ---- the way in -------------------------------------------------------------------------------------------------------

# TLS at the load balancer with the organisation's certificate (ACM, in this Region), and TLS again to the server, which
# presents its own certificate: nothing between the client and Perfuse carries the traffic in clear. MLLP ports are the
# same, so partners connect with TLS to a certificate they can check.
resource "aws_lb" "main" {
  name                             = local.name
  load_balancer_type               = "network"
  internal                         = false
  subnets                          = aws_subnet.public[*].id
  enable_cross_zone_load_balancing = true
  security_groups                  = [aws_security_group.lb.id]
}

resource "aws_security_group" "lb" {
  name        = "${local.name}-lb"
  description = "The load balancer: the console, FHIR and MLLP"
  vpc_id      = aws_vpc.main.id
  dynamic "ingress" {
    for_each = concat([443], var.mllp_ports)
    content {
      description = "TLS ${ingress.value}"
      from_port   = ingress.value
      to_port     = ingress.value
      protocol    = "tcp"
      cidr_blocks = local.ingress_cidrs
    }
  }
  egress {
    description = "To the server"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.vpc_cidr]
  }
}

locals {
  # listener port -> server port
  forwards = merge({ "443" = 8443 }, { for p in var.mllp_ports : tostring(p) => p })
}

resource "aws_lb_target_group" "main" {
  for_each             = local.forwards
  name                 = "${substr(local.name, 0, 20)}-${each.key}"
  port                 = each.value
  protocol             = "TLS"
  vpc_id               = aws_vpc.main.id
  target_type          = "instance"
  preserve_client_ip   = "true"
  deregistration_delay = 30
  # HTTPS to /readyz, not a bare TCP or TLS connection: readiness covers the database, and a connection opened and closed
  # without a request is logged by the server as a failed TLS handshake every few seconds.
  health_check {
    protocol = "HTTPS"
    port     = "8443"
    path     = "/readyz"
    matcher  = "200"
  }
}

resource "aws_lb_target_group_attachment" "main" {
  for_each         = local.forwards
  target_group_arn = aws_lb_target_group.main[each.key].arn
  target_id        = aws_instance.server.id
  port             = each.value
}

resource "aws_lb_listener" "main" {
  for_each          = local.forwards
  load_balancer_arn = aws_lb.main.arn
  port              = tonumber(each.key)
  protocol          = "TLS"
  certificate_arn   = var.certificate_arn
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.main[each.key].arn
  }
}
