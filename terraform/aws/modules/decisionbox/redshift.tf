resource "aws_iam_role_policy" "redshift" {
  count = var.enable_redshift_iam ? 1 : 0

  name = "redshift-read"
  role = aws_iam_role.irsa_agent.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "RedshiftDataRead"
        Effect = "Allow"
        Action = [
          "redshift-data:ExecuteStatement",
          "redshift-data:GetStatementResult",
          "redshift-data:DescribeStatement",
          "redshift-data:ListStatements",
          "redshift-data:CancelStatement",
          "redshift-data:ListTables",
          "redshift-data:DescribeTable",
          "redshift-data:ListSchemas",
          "redshift-data:ListDatabases",
          "redshift:GetClusterCredentials",
          "redshift:DescribeClusters",
          "redshift-serverless:GetCredentials",
          "redshift-serverless:ListWorkgroups",
        ]
        Resource = "*"
      },
    ]
  })
}
