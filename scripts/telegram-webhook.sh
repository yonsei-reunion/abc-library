#!/usr/bin/env bash
# bandlogin 서비스 주소를 텔레그램 봇의 웹훅으로 등록한다. 서비스를 처음 배포한 뒤나 웹훅 비밀 토큰을 바꾼 뒤에 실행한다.
#
#   make telegram-webhook
#
# 봇 토큰과 웹훅 비밀 토큰은 Secret Manager에서 읽어서 셸 변수로만 쓰고 출력하지 않는다.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=gcp-env.sh
source "$root/scripts/gcp-env.sh"

url="$(gcloud run services describe "$LOGIN_SERVICE" --region "$LOGIN_REGION" --project "$PROJECT" --format 'value(status.url)')"
if [[ -z "$url" ]]; then
  echo "$LOGIN_SERVICE 서비스가 없습니다. make deploy-bandlogin으로 먼저 배포하세요." >&2
  exit 1
fi
token="$(gcloud secrets versions access latest --secret "$TELEGRAM_TOKEN_SECRET" --project "$PROJECT")"
hook_secret="$(gcloud secrets versions access latest --secret "$TELEGRAM_WEBHOOK_SECRET_NAME" --project "$PROJECT")"

# 봇 토큰이 명령줄 인자(프로세스 목록)에 드러나지 않도록 curl 설정을 표준 입력으로 넘긴다.
resp="$(curl -sS --config - <<EOF
url = "https://api.telegram.org/bot${token}/setWebhook"
data-urlencode = "url=${url}/telegram"
data-urlencode = "secret_token=${hook_secret}"
data-urlencode = "allowed_updates=[\"message\"]"
data-urlencode = "drop_pending_updates=true"
EOF
)"
if [[ "$resp" != *'"ok":true'* ]]; then
  echo "웹훅을 등록하지 못했습니다: $resp" >&2
  exit 1
fi
echo "웹훅을 등록했습니다: ${url}/telegram"
