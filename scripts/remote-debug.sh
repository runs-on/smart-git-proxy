#!/bin/bash
set -ex

S3_BUCKET="${S3_BUCKET:-runs-on}"
BINARY_PATH="bin/smart-git-proxy-linux-arm64"
S3_KEY="tmp/smart-git-proxy"

# Auto-discover instance ID if not provided
if [ -z "$INSTANCE_ID" ]; then
    echo "Discovering instance..."
    INSTANCE_ID=$(aws ec2 describe-instances \
        --filters "Name=tag:Name,Values=smart-git-proxy" "Name=instance-state-name,Values=running" \
        --query 'Reservations[0].Instances[0].InstanceId' --output text)
fi

if [ "$INSTANCE_ID" = "None" ] || [ -z "$INSTANCE_ID" ]; then
    echo "Error: No running instance found with Name=smart-git-proxy"
    exit 1
fi

echo "Instance: $INSTANCE_ID"

echo "Uploading binary to S3..."
AWS_PROFILE=runs-on-releaser aws s3 cp "$BINARY_PATH" "s3://$S3_BUCKET/$S3_KEY"

echo "Generating presigned URL..."
PRESIGNED_URL=$(AWS_PROFILE=runs-on-releaser aws s3 presign --region eu-west-1 "s3://$S3_BUCKET/$S3_KEY" --expires-in 300)

tmpfile=$(mktemp)
trap "rm -f $tmpfile" EXIT

cat <<EOF > $tmpfile
systemctl stop smart-git-proxy
curl -fL '$PRESIGNED_URL' -o /usr/bin/smart-git-proxy
chmod +x /usr/bin/smart-git-proxy
systemctl restart smart-git-proxy
EOF

echo "Deploying to instance..."
COMMAND_ID=$(aws ssm send-command \
    --instance-ids "$INSTANCE_ID" \
    --document-name "AWS-RunShellScript" \
    --parameters "{\"commands\":[\"systemctl stop smart-git-proxy\",\"curl -fL '$PRESIGNED_URL' -o /usr/bin/smart-git-proxy\",\"chmod +x /usr/bin/smart-git-proxy\",\"systemctl restart smart-git-proxy\"]}" \
    --output text --query 'Command.CommandId')

echo "Waiting for deployment (command: $COMMAND_ID)..."
aws ssm wait command-executed --instance-id "$INSTANCE_ID" --command-id "$COMMAND_ID"
aws ssm get-command-invocation --command-id "$COMMAND_ID" --instance-id "$INSTANCE_ID" --query '{Status:Status,Output:StandardOutputContent,Error:StandardErrorContent}'

cat $tmpfile

echo "Opening session..."
aws ssm start-session --target "$INSTANCE_ID" --document-name AWS-StartInteractiveCommand --parameters command="sudo -i"
