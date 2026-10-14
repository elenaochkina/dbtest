# The JSON files are the source of truth, so an admin who does not use
# Terraform can create the same policies by hand.
resource "aws_iam_policy" "deployer" {
  name        = "dbtest-deployer"
  description = "Apply terraform/aws and push dbtest images"
  policy      = file("${path.module}/dbtest-deployer.json")
}

resource "aws_iam_policy" "runner" {
  name        = "dbtest-runner"
  description = "Create, disrupt and delete dbtest databases and run its Fargate tasks"
  policy      = file("${path.module}/dbtest-runner.json")
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

resource "aws_iam_role" "deployer" {
  name               = "dbtest-deployer"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "deployer" {
  role       = aws_iam_role.deployer.name
  policy_arn = aws_iam_policy.deployer.arn
}

resource "aws_iam_role" "runner" {
  name               = "dbtest-runner"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "runner" {
  role       = aws_iam_role.runner.name
  policy_arn = aws_iam_policy.runner.arn
}
