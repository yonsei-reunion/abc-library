#!/usr/bin/env bash
# 이미지를 빌드해서 Artifact Registry에 올리고 Cloud Run에 배포한다.
#
#   scripts/deploy.sh web     웹 서버(Cloud Run 서비스)
#   scripts/deploy.sh batch   일일 배치와 대화방 주간 요약(Cloud Run Job)
#   scripts/deploy.sh bandlogin   폰에서 밴드에 다시 로그인하는 서비스(Cloud Run 서비스). gcp-env.sh의 TELEGRAM_CHAT_ID가 있어야 한다
#   scripts/deploy.sh web --if-changed   배포된 이미지의 커밋 이후 웹에 들어가는 파일이 바뀌었을 때만 배포한다
#
# 환경 변수와 비밀 값 연결은 서비스와 Job에 남아서 다음 배포에도 이어진다.
# DB_URL이 환경 변수에 있으면(로컬에서 .envrc를 읽은 경우) 그 값으로 바꾸고, 없으면(GitHub Actions) 그대로 둔다.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=gcp-env.sh
source "$root/scripts/gcp-env.sh"
cd "$root"

target="${1:-}"
case "$target" in
  web) region="$WEB_REGION" ;;
  batch) region="$BATCH_REGION" ;;
  bandlogin) region="$LOGIN_REGION" ;;
  *)
    echo "사용법: $0 <web|batch|bandlogin> [--if-changed]" >&2
    exit 2
    ;;
esac

if [[ "$target" == bandlogin && -z "$TELEGRAM_CHAT_ID" ]]; then
  echo "gcp-env.sh의 TELEGRAM_CHAT_ID가 비어 있어서 bandlogin을 배포하지 않습니다."
  exit 0
fi

# 이미지 태그가 커밋 번호이므로 배포된 커밋과 지금 커밋을 비교한다. 대상 바이너리가 쓰는 이 모듈의 패키지
# 디렉터리(템플릿, 정적 파일, 마이그레이션처럼 바이너리에 넣는 파일도 그 아래에 있다)와 빌드, 배포 설정을 본다.
# 배포된 커밋을 알 수 없으면(처음 배포, 로컬에서 고친 채 배포한 이미지) 배포한다.
if [[ "${2:-}" == --if-changed ]]; then
  if [[ "$target" == web || "$target" == bandlogin ]]; then
    service="$WEB_SERVICE"
    [[ "$target" == bandlogin ]] && service="$LOGIN_SERVICE"
    deployed="$(gcloud run services describe "$service" --region "$region" --project "$PROJECT" \
      --format 'value(spec.template.spec.containers[0].image)' 2>/dev/null || true)"
  else
    deployed="$(gcloud run jobs describe "$BATCH_JOB" --region "$region" --project "$PROJECT" \
      --format 'value(spec.template.spec.template.spec.containers[0].image)' 2>/dev/null || true)"
  fi
  commit="${deployed##*:}"
  if [[ "$commit" =~ ^[0-9a-f]{12}$ ]] && git cat-file -e "${commit}^{commit}" 2>/dev/null; then
    dirs="$(go list -deps -f '{{if not .Standard}}{{.Dir}}{{end}}' "./cmd/$target" | sed -n "s|^$root/||p")"
    mapfile -t paths <<<"$dirs"
    paths+=(go.mod go.sum "Dockerfile.$target" .dockerignore scripts/deploy.sh scripts/gcp-env.sh)
    # 테스트와 문서는 이미지에 들어가지 않는다.
    changed="$(git diff --name-only "$commit" HEAD -- "${paths[@]}" \
      ':(exclude,glob)**/*_test.go' ':(exclude,glob)**/testdata/**' ':(exclude,glob)**/*.md')"
    if [[ -z "$changed" ]]; then
      echo "배포된 커밋 ${commit} 이후 ${target}에 들어가는 파일이 바뀌지 않아서 배포하지 않습니다."
      exit 0
    fi
    echo "배포된 커밋 ${commit} 이후 바뀐 ${target} 파일:"
    sed 's/^/  /' <<<"$changed"
  else
    echo "배포된 커밋을 알 수 없어서 배포합니다: ${deployed:-배포된 이미지 없음}"
  fi
fi

tag="$(git rev-parse --short=12 HEAD)"
# 추적하지 않는 파일(GitHub Actions의 gha-creds-*.json 등)은 이미지에 들어가지 않으므로 보지 않는다.
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  tag="${tag}-dirty-$(date +%Y%m%d%H%M%S)"
fi
registry="${region}-docker.pkg.dev"
image="${registry}/${PROJECT}/${REPO}/${target}:${tag}"

gcloud auth configure-docker "$registry" --quiet >/dev/null 2>&1
docker build -f "Dockerfile.${target}" -t "$image" .
docker push "$image"

# 값에 쉼표가 들어가도 되도록 구분자를 |로 바꾼다.
env_vars=()
[[ -n "${DB_URL:-}" ]] && env_vars+=("DB_URL=${DB_URL}")

if [[ "$target" == web ]]; then
  flags=()
  secrets="DB_PWD=${DB_PWD_SECRET}:latest"
  # 로그인. GOOGLE_CLIENT_ID가 비어 있으면 로그인 없이 배포한다.
  if [[ -n "$GOOGLE_CLIENT_ID" ]]; then
    env_vars+=("GOOGLE_CLIENT_ID=${GOOGLE_CLIENT_ID}" "SITE_URL=${SITE_URL}")
    secrets+=",GOOGLE_CLIENT_SECRET=${GOOGLE_SECRET}:latest,SESSION_SECRET=${SESSION_SECRET_NAME}:latest"
    secrets+=",USER_ID_SECRET=${USER_ID_SECRET_NAME}:latest"
  fi
  # 공개 접근은 처음 만들 때만 설정한다. GitHub Actions의 배포 계정은 IAM 정책을 바꾸지 못한다.
  if ! gcloud run services describe "$WEB_SERVICE" --region "$region" --project "$PROJECT" >/dev/null 2>&1; then
    flags+=(--allow-unauthenticated)
  fi
  ((${#env_vars[@]})) && flags+=(--update-env-vars "^|^$(IFS='|'; echo "${env_vars[*]}")")
  gcloud run deploy "$WEB_SERVICE" --image "$image" --region "$region" --project "$PROJECT" \
    --service-account "$WEB_SA" \
    --min-instances 0 --max-instances 3 \
    --set-secrets "$secrets" \
    "${flags[@]}"
  gcloud run services describe "$WEB_SERVICE" --region "$region" --project "$PROJECT" --format 'value(status.url)'
elif [[ "$target" == bandlogin ]]; then
  flags=()
  # 텔레그램 웹훅과 폰 브라우저가 부르므로 공개한다. 막는 것은 웹훅 비밀 토큰, 대화방 번호, 1회용 링크다.
  if ! gcloud run services describe "$LOGIN_SERVICE" --region "$region" --project "$PROJECT" >/dev/null 2>&1; then
    flags+=(--allow-unauthenticated)
  fi
  # 로그인하는 몇 분 동안만 켜진다. 브라우저와 링크 토큰이 메모리에 있으므로 인스턴스는 하나만 둔다.
  # 화면 스트림(SSE)이 연결 하나를 계속 쓰므로 입력 요청이 함께 들어오게 동시성을 1보다 크게 둔다.
  # 요청이 없는 동안에도 로그인 감지와 시간 초과 처리가 돌도록 CPU를 늘 쓴다.
  gcloud run deploy "$LOGIN_SERVICE" --image "$image" --region "$region" --project "$PROJECT" \
    --service-account "$LOGIN_SA" \
    --min-instances 0 --max-instances 1 --concurrency 10 --timeout 900 \
    --cpu 1 --memory 2Gi --no-cpu-throttling \
    --set-secrets "TELEGRAM_BOT_TOKEN=${TELEGRAM_TOKEN_SECRET}:latest,TELEGRAM_WEBHOOK_SECRET=${TELEGRAM_WEBHOOK_SECRET_NAME}:latest" \
    --update-env-vars "GOOGLE_CLOUD_PROJECT=${PROJECT},BAND_STATE_SECRET=${BAND_STATE_SECRET},TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID}" \
    "${flags[@]}"
  gcloud run services describe "$LOGIN_SERVICE" --region "$region" --project "$PROJECT" --format 'value(status.url)'
else
  env_vars+=("BAND_STATE_PATH=${BAND_STATE_MOUNT}")
  for name in ANTHROPIC_FEDERATION_RULE_ID ANTHROPIC_ORGANIZATION_ID ANTHROPIC_SERVICE_ACCOUNT_ID ANTHROPIC_WORKSPACE_ID; do
    [[ -n "${!name}" ]] && env_vars+=("${name}=${!name}")
  done
  secrets="DB_PWD=${DB_PWD_SECRET}:latest,${BAND_STATE_MOUNT}=${BAND_STATE_SECRET}:latest"
  # 대화방 주간 요약(batch weekly)
  secrets+=",KAKAO_SHEET_URL=${KAKAO_SHEET_URL_SECRET}:latest,KAKAO_SHEET_TOKEN=${KAKAO_SHEET_TOKEN_SECRET}:latest"
  # 세션이 곧 만료되거나 만료로 실패하면 bandlogin과 같은 봇으로 알린다(cmd/batch/alert.go).
  if [[ -n "$TELEGRAM_CHAT_ID" ]]; then
    secrets+=",TELEGRAM_BOT_TOKEN=${TELEGRAM_TOKEN_SECRET}:latest"
    env_vars+=("TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID}")
  fi
  # Workload Identity Federation 값이 없으면 Claude API 키를 쓴다.
  [[ -z "$ANTHROPIC_FEDERATION_RULE_ID" ]] && secrets+=",CLAUDE_API_KEY=${CLAUDE_KEY_SECRET}:latest"
  # 자동 재시도는 하지 않는다. 실패한 글은 다음 날 배치가 다시 시도한다.
  gcloud run jobs deploy "$BATCH_JOB" --image "$image" --region "$region" --project "$PROJECT" \
    --service-account "$BATCH_SA" \
    --cpu 2 --memory 2Gi --task-timeout 1h --max-retries 0 --tasks 1 \
    --set-secrets "$secrets" \
    --update-env-vars "^|^$(IFS='|'; echo "${env_vars[*]}")"
fi
