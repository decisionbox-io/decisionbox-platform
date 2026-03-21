data "aws_caller_identity" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = length(var.availability_zones) > 0 ? var.availability_zones : slice(data.aws_availability_zones.available.names, 0, 3)

  vpc_id             = var.create_vpc ? aws_vpc.main[0].id : var.existing_vpc_id
  private_subnet_ids = var.create_vpc ? aws_subnet.private[*].id : var.existing_private_subnet_ids
  public_subnet_ids  = var.create_vpc ? aws_subnet.public[*].id : var.existing_public_subnet_ids

  common_tags = merge(var.tags, {
    project    = "decisionbox"
    managed_by = "terraform"
  })
}

# ─── VPC ──────────────────────────────────────────────────────────────────────

resource "aws_vpc" "main" {
  count = var.create_vpc ? 1 : 0

  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-vpc"
  })
}

# ─── Subnets ──────────────────────────────────────────────────────────────────

resource "aws_subnet" "private" {
  count = var.create_vpc ? length(local.azs) : 0

  vpc_id            = aws_vpc.main[0].id
  cidr_block        = var.private_subnet_cidrs[count.index]
  availability_zone = local.azs[count.index]

  tags = merge(local.common_tags, {
    Name                                        = "${var.cluster_name}-private-${local.azs[count.index]}"
    "kubernetes.io/role/internal-elb"           = "1"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  })
}

resource "aws_subnet" "public" {
  count = var.create_vpc ? length(local.azs) : 0

  vpc_id                  = aws_vpc.main[0].id
  cidr_block              = var.public_subnet_cidrs[count.index]
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true

  tags = merge(local.common_tags, {
    Name                                        = "${var.cluster_name}-public-${local.azs[count.index]}"
    "kubernetes.io/role/elb"                    = "1"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  })
}

# ─── Internet Gateway ────────────────────────────────────────────────────────

resource "aws_internet_gateway" "main" {
  count = var.create_vpc ? 1 : 0

  vpc_id = aws_vpc.main[0].id

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-igw"
  })
}

# ─── NAT Gateway ─────────────────────────────────────────────────────────────

resource "aws_eip" "nat" {
  count = var.create_vpc ? (var.single_nat_gateway ? 1 : length(local.azs)) : 0

  domain = "vpc"

  tags = merge(local.common_tags, {
    Name = var.single_nat_gateway ? "${var.cluster_name}-nat" : "${var.cluster_name}-nat-${local.azs[count.index]}"
  })
}

resource "aws_nat_gateway" "main" {
  count = var.create_vpc ? (var.single_nat_gateway ? 1 : length(local.azs)) : 0

  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id

  tags = merge(local.common_tags, {
    Name = var.single_nat_gateway ? "${var.cluster_name}-nat" : "${var.cluster_name}-nat-${local.azs[count.index]}"
  })

  depends_on = [aws_internet_gateway.main]
}

# ─── Route Tables ────────────────────────────────────────────────────────────

resource "aws_route_table" "public" {
  count = var.create_vpc ? 1 : 0

  vpc_id = aws_vpc.main[0].id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main[0].id
  }

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-public"
  })
}

resource "aws_route_table_association" "public" {
  count = var.create_vpc ? length(local.azs) : 0

  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_route_table" "private" {
  count = var.create_vpc ? (var.single_nat_gateway ? 1 : length(local.azs)) : 0

  vpc_id = aws_vpc.main[0].id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.main[count.index].id
  }

  tags = merge(local.common_tags, {
    Name = var.single_nat_gateway ? "${var.cluster_name}-private" : "${var.cluster_name}-private-${local.azs[count.index]}"
  })
}

resource "aws_route_table_association" "private" {
  count = var.create_vpc ? length(local.azs) : 0

  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[var.single_nat_gateway ? 0 : count.index].id
}

# ─── VPC Flow Logs ───────────────────────────────────────────────────────────

resource "aws_flow_log" "main" {
  count = var.create_vpc && var.enable_flow_logs ? 1 : 0

  vpc_id                   = aws_vpc.main[0].id
  traffic_type             = "ALL"
  iam_role_arn             = aws_iam_role.flow_log[0].arn
  log_destination          = aws_cloudwatch_log_group.flow_log[0].arn
  log_destination_type     = "cloud-watch-logs"
  max_aggregation_interval = 600

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-flow-log"
  })
}

resource "aws_cloudwatch_log_group" "flow_log" {
  count = var.create_vpc && var.enable_flow_logs ? 1 : 0

  name              = "/aws/vpc/flow-log/${var.cluster_name}"
  retention_in_days = var.flow_log_retention_days
  kms_key_id        = aws_kms_key.flow_log[0].arn

  tags = local.common_tags
}

resource "aws_kms_key" "flow_log" {
  count = var.create_vpc && var.enable_flow_logs ? 1 : 0

  description         = "VPC flow log encryption key for ${var.cluster_name}"
  enable_key_rotation = true

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "AllowAccountRoot"
        Effect = "Allow"
        Principal = {
          AWS = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"
        }
        Action   = "kms:*"
        Resource = "*"
      },
      {
        Sid    = "AllowCloudWatchLogs"
        Effect = "Allow"
        Principal = {
          Service = "logs.${var.region}.amazonaws.com"
        }
        Action = [
          "kms:Encrypt",
          "kms:Decrypt",
          "kms:GenerateDataKey*",
          "kms:DescribeKey",
        ]
        Resource = "*"
        Condition = {
          ArnLike = {
            "kms:EncryptionContext:aws:logs:arn" = "arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/vpc/flow-log/${var.cluster_name}"
          }
        }
      },
    ]
  })

  tags = local.common_tags
}

resource "aws_iam_role" "flow_log" {
  count = var.create_vpc && var.enable_flow_logs ? 1 : 0

  name = "${var.cluster_name}-flow-log"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action = "sts:AssumeRole"
      Effect = "Allow"
      Principal = {
        Service = "vpc-flow-logs.amazonaws.com"
      }
    }]
  })

  tags = local.common_tags
}

resource "aws_iam_role_policy" "flow_log" {
  count = var.create_vpc && var.enable_flow_logs ? 1 : 0

  name = "flow-log-cloudwatch"
  role = aws_iam_role.flow_log[0].id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action = [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents",
        "logs:DescribeLogGroups",
        "logs:DescribeLogStreams",
      ]
      Effect   = "Allow"
      Resource = "${aws_cloudwatch_log_group.flow_log[0].arn}:*"
    }]
  })
}
