# The JSON files are the source of truth, so an admin who does not use
# Terraform can create the same policies by hand. They stay two policies
# because one would be close to IAM's 6,144-character limit.
resource "aws_iam_policy" "infra" {
  name        = "dbtest-infra"
  description = "Apply terraform/aws and push dbtest images"
  policy      = file("${path.module}/dbtest-infra.json")
}

resource "aws_iam_policy" "run" {
  name        = "dbtest-run"
  description = "Create, disrupt and delete dbtest databases and run its Fargate tasks"
  policy      = file("${path.module}/dbtest-run.json")
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = var.trusted_principals
    }
  }
}

resource "aws_iam_role" "dbtest" {
  name               = "dbtest"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "infra" {
  role       = aws_iam_role.dbtest.name
  policy_arn = aws_iam_policy.infra.arn
}

resource "aws_iam_role_policy_attachment" "run" {
  role       = aws_iam_role.dbtest.name
  policy_arn = aws_iam_policy.run.arn
}
