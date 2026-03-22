# Secrets Manager IAM — grants the API and Agent IRSA roles permission to
# access secrets scoped to the configured namespace prefix.
# The API creates and manages secrets at runtime (not Terraform).
# The Agent reads secrets (e.g., LLM API keys) during discovery runs.

locals {
  secrets_resource_arn = "arn:aws:secretsmanager:${var.region}:${data.aws_caller_identity.current.account_id}:secret:${var.secret_namespace}/*"

  # Full read/write policy for the API (creates, updates, deletes secrets).
  secrets_api_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListSecrets"
        Effect   = "Allow"
        Action   = ["secretsmanager:ListSecrets"]
        Resource = "*"
      },
      {
        Sid    = "ManageNamespacedSecrets"
        Effect = "Allow"
        Action = [
          "secretsmanager:CreateSecret",
          "secretsmanager:GetSecretValue",
          "secretsmanager:PutSecretValue",
          "secretsmanager:DescribeSecret",
          "secretsmanager:UpdateSecret",
          "secretsmanager:DeleteSecret",
          "secretsmanager:TagResource",
        ]
        Resource = local.secrets_resource_arn
      },
    ]
  })

  # Read-only policy for the Agent (reads LLM/warehouse credentials).
  secrets_agent_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ListSecrets"
        Effect   = "Allow"
        Action   = ["secretsmanager:ListSecrets"]
        Resource = "*"
      },
      {
        Sid    = "ReadNamespacedSecrets"
        Effect = "Allow"
        Action = [
          "secretsmanager:GetSecretValue",
          "secretsmanager:DescribeSecret",
        ]
        Resource = local.secrets_resource_arn
      },
    ]
  })
}

resource "aws_iam_role_policy" "secrets_manager" {
  count = var.enable_aws_secrets ? 1 : 0

  name   = "secrets-manager"
  role   = aws_iam_role.irsa_api.id
  policy = local.secrets_api_policy
}

resource "aws_iam_role_policy" "secrets_manager_agent" {
  count = var.enable_aws_secrets ? 1 : 0

  name   = "secrets-manager"
  role   = aws_iam_role.irsa_agent.id
  policy = local.secrets_agent_policy
}
