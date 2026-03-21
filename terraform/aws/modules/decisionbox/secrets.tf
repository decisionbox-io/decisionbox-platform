# Secrets Manager IAM — grants the API's IRSA role permission to
# create, read, and list secrets scoped to the configured namespace prefix.
# The API itself creates and manages secrets at runtime (not Terraform).

resource "aws_iam_role_policy" "secrets_manager" {
  count = var.enable_aws_secrets ? 1 : 0

  name = "secrets-manager"
  role = aws_iam_role.irsa_api.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ListSecrets"
        Effect = "Allow"
        Action = [
          "secretsmanager:ListSecrets",
        ]
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
        Resource = "arn:aws:secretsmanager:${var.region}:${data.aws_caller_identity.current.account_id}:secret:${var.secret_namespace}/*"
      },
    ]
  })
}
