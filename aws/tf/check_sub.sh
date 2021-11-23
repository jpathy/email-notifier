#!/usr/bin/env bash
set -e

eval "$(jq -r '@sh "STMT=\(.partiql_stmt) REGION=\(.region)"')"
COUNT=$(aws dynamodb execute-statement --statement "$STMT" --region "$REGION" --query "length(Items[*])")
jq -n --arg count "$COUNT" '{"count":$count}'
