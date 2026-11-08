# Google Cloud 배포 스크립트가 함께 쓰는 값이다. 다른 스크립트에서 source로 읽는다.
# 비밀 값은 없다. 비밀 값은 Secret Manager에 두고 이름으로만 가리킨다.

# gcloud가 PATH에 없으면 기본 설치 경로를 잡는다.
command -v gcloud >/dev/null 2>&1 || PATH="$HOME/google-cloud-sdk/bin:$PATH"

PROJECT=abc-library-512606
WEB_REGION=asia-southeast1   # 싱가포르. Neon DB와 같은 지역이다.
BATCH_REGION=asia-northeast3 # 서울. 밴드 세션이 한국 IP에서 쓰이게 한다.

# Artifact Registry 저장소. 이미지를 실행하는 지역마다 같은 이름으로 하나씩 둔다.
REPO=abc-library

WEB_SERVICE=abc-library
# Firebase Hosting 사이트. https://abc-lib.web.app이 웹 서비스로 요청을 넘긴다.
FIREBASE_SITE=abc-lib
SITE_URL=https://abc-lib.web.app

# 관리자 로그인에 쓰는 Google OAuth 클라이언트 ID. 비밀이 아니다(로그인 화면 주소에 그대로 나온다).
# 비워 두면 웹 서버가 관리자 로그인을 쓰지 않는다. 만드는 절차는 docs/spec.md의 '관리자 로그인 설정'에 있다.
GOOGLE_CLIENT_ID="185613497626-0st92809g1a0v1sqjgovtu81hr874a3v.apps.googleusercontent.com"
BATCH_JOB=abc-batch
SCHEDULER_JOB=abc-batch-daily
# 같은 배치 Job을 월요일 아침에 batch weekly로 실행해서 카카오톡 대화방 주간 요약을 밴드에 올린다.
WEEKLY_SCHEDULER_JOB=abc-batch-weekly

WEB_SA="abc-web@${PROJECT}.iam.gserviceaccount.com"
BATCH_SA="abc-batch@${PROJECT}.iam.gserviceaccount.com"
SCHEDULER_SA="abc-scheduler@${PROJECT}.iam.gserviceaccount.com"
DEPLOY_SA="abc-deployer@${PROJECT}.iam.gserviceaccount.com"

# Secret Manager의 비밀 이름
DB_PWD_SECRET=db-pwd
BAND_STATE_SECRET=band-state
# 배치가 Claude를 부를 때 쓰던 API 키. 아래 Workload Identity Federation 값이 있으면 Job에 연결하지 않는다.
CLAUDE_KEY_SECRET=claude-api-key
# 관리자 로그인(Google OAuth 클라이언트 보안 비밀)과 세션 쿠키 서명에 쓰는 비밀. 웹 서버만 읽는다.
GOOGLE_SECRET=google-client-secret
SESSION_SECRET_NAME=session-secret
# 사용자를 구별하려고 Google 계정 고유 번호(sub)를 HMAC-SHA256할 때 쓰는 키. 바꾸면 모든 사용자가 북마크를 잃는다.
USER_ID_SECRET_NAME=user-id-secret
# 대화방 주간 요약(batch weekly)이 메시지를 읽는 Google 스프레드시트의 Apps Script 웹 앱 주소와 토큰. 배치만 읽는다.
# 요약은 수집과 같은 밴드 세션(band-state)으로 밴드에 올리므로 다른 비밀은 없다.
KAKAO_SHEET_URL_SECRET=kakao-sheet-url
KAKAO_SHEET_TOKEN_SECRET=kakao-sheet-token
# 배치 Job에서 band-state 비밀을 파일로 올리는 경로
BAND_STATE_MOUNT=/secrets/band/band-state.json

# GitHub Actions가 서비스 계정 키 없이 배포하도록 쓰는 Workload Identity Federation
GITHUB_REPO=yonsei-reunion/abc-library
# 이 브랜치에 push된 워크플로만 배포 토큰을 받는다. .github/workflows/ci.yml의 deploy 조건과 맞춘다.
DEPLOY_BRANCH=release
WIF_POOL=github
WIF_PROVIDER=abc-library

# 배치가 Claude를 Workload Identity Federation으로 호출할 때 쓰는 값이다.
# Claude Console의 Workload identity 규칙 abc-batch에서 나온 ID라서 비밀이 아니다. 규칙은 abc-batch 서비스 계정의
# Google ID 토큰을 Console 서비스 계정 abc-batch로 바꿔 주고, 토큰은 abc-library 워크스페이스(월 지출 한도 $200)에서만 쓴다.
# 값이 있으면 extract.NewClient가 API 키 대신 WIF를 쓴다. 비우고 배포하면 claude-api-key로 돌아간다.
ANTHROPIC_FEDERATION_RULE_ID="fdrl_01VMxpGzcr5veU12AxcDqZBp"
ANTHROPIC_ORGANIZATION_ID="effc7f0b-884f-4d61-87ef-d244fa71c789"
ANTHROPIC_SERVICE_ACCOUNT_ID="svac_01SmHw7Zb9N4QvRpYRzvXjiM"
ANTHROPIC_WORKSPACE_ID="wrkspc_014xvAz2r2XbW3Uu11hyxtuH"
