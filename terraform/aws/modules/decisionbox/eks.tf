data "aws_eks_cluster" "existing" {
  count = var.create_cluster ? 0 : 1
  name  = var.cluster_name
}

# ─── EKS Cluster ─────────────────────────────────────────────────────────────

resource "aws_eks_cluster" "main" {
  count = var.create_cluster ? 1 : 0

  name     = var.cluster_name
  role_arn = aws_iam_role.eks_cluster[0].arn
  version  = var.kubernetes_version

  vpc_config {
    subnet_ids              = concat(local.private_subnet_ids, local.public_subnet_ids)
    endpoint_private_access = var.endpoint_private_access
    endpoint_public_access  = var.endpoint_public_access
    public_access_cidrs     = var.public_access_cidrs
    security_group_ids      = [aws_security_group.eks_cluster[0].id]
  }

  enabled_cluster_log_types = var.enabled_cluster_log_types

  access_config {
    authentication_mode = "API_AND_CONFIG_MAP"
  }

  encryption_config {
    provider {
      key_arn = aws_kms_key.eks[0].arn
    }
    resources = ["secrets"]
  }

  tags = merge(local.common_tags, {
    Name = var.cluster_name
  })

  depends_on = [
    aws_iam_role_policy_attachment.eks_cluster_policy,
    aws_iam_role_policy_attachment.eks_service_policy,
    aws_cloudwatch_log_group.eks,
  ]
}

# Grant the Terraform caller cluster admin access via EKS Access API
resource "aws_eks_access_entry" "admin" {
  count = var.create_cluster ? 1 : 0

  cluster_name  = aws_eks_cluster.main[0].name
  principal_arn = data.aws_caller_identity.current.arn
  type          = "STANDARD"
}

resource "aws_eks_access_policy_association" "admin" {
  count = var.create_cluster ? 1 : 0

  cluster_name  = aws_eks_cluster.main[0].name
  principal_arn = data.aws_caller_identity.current.arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"

  access_scope {
    type = "cluster"
  }

  depends_on = [aws_eks_access_entry.admin]
}

resource "aws_kms_key" "eks" {
  count = var.create_cluster ? 1 : 0

  description         = "EKS encryption key for ${var.cluster_name} (secrets + logs)"
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
            "kms:EncryptionContext:aws:logs:arn" = "arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/eks/${var.cluster_name}/*"
          }
        }
      },
    ]
  })

  tags = local.common_tags
}

resource "aws_kms_alias" "eks" {
  count = var.create_cluster ? 1 : 0

  name          = "alias/${var.cluster_name}-eks"
  target_key_id = aws_kms_key.eks[0].key_id
}

resource "aws_cloudwatch_log_group" "eks" {
  count = var.create_cluster ? 1 : 0

  name              = "/aws/eks/${var.cluster_name}/cluster"
  retention_in_days = var.log_retention_days
  kms_key_id        = aws_kms_key.eks[0].arn

  tags = local.common_tags
}

# ─── EBS CSI Driver Addon ────────────────────────────────────────────────────

resource "aws_eks_addon" "ebs_csi" {
  count = var.create_cluster ? 1 : 0

  cluster_name             = aws_eks_cluster.main[0].name
  addon_name               = "aws-ebs-csi-driver"
  service_account_role_arn = aws_iam_role.ebs_csi[0].arn

  depends_on = [aws_eks_node_group.main]
}

# ─── Cluster Security Group ─────────────────────────────────────────────────

resource "aws_security_group" "eks_cluster" {
  count = var.create_cluster ? 1 : 0

  name_prefix = "${var.cluster_name}-cluster-"
  vpc_id      = local.vpc_id
  description = "EKS cluster security group"

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-cluster"
  })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_security_group_rule" "cluster_egress" {
  count = var.create_cluster ? 1 : 0

  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  cidr_blocks       = ["0.0.0.0/0"]
  security_group_id = aws_security_group.eks_cluster[0].id
  description       = "Allow all outbound"
}

# ─── Managed Node Group ──────────────────────────────────────────────────────

resource "aws_eks_node_group" "main" {
  count = var.create_cluster ? 1 : 0

  cluster_name    = aws_eks_cluster.main[0].name
  node_group_name = "${var.cluster_name}-nodes"
  node_role_arn   = aws_iam_role.eks_nodes[0].arn
  subnet_ids      = local.private_subnet_ids

  ami_type       = var.ami_type
  instance_types = [var.instance_type]
  disk_size      = var.disk_size_gb

  scaling_config {
    min_size     = var.min_node_count
    max_size     = var.max_node_count
    desired_size = var.desired_node_count
  }

  update_config {
    max_unavailable = 1
  }

  tags = merge(local.common_tags, {
    Name = "${var.cluster_name}-nodes"
  })

  depends_on = [
    aws_iam_role_policy_attachment.eks_worker_node_policy,
    aws_iam_role_policy_attachment.eks_cni_policy,
    aws_iam_role_policy_attachment.eks_container_registry,
  ]
}

# ─── Locals ──────────────────────────────────────────────────────────────────

locals {
  cluster_name     = var.create_cluster ? aws_eks_cluster.main[0].name : data.aws_eks_cluster.existing[0].name
  cluster_endpoint = var.create_cluster ? aws_eks_cluster.main[0].endpoint : data.aws_eks_cluster.existing[0].endpoint
  cluster_ca       = var.create_cluster ? aws_eks_cluster.main[0].certificate_authority[0].data : data.aws_eks_cluster.existing[0].certificate_authority[0].data
  oidc_issuer      = var.create_cluster ? aws_eks_cluster.main[0].identity[0].oidc[0].issuer : data.aws_eks_cluster.existing[0].identity[0].oidc[0].issuer
}
