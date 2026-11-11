# 밴드 재로그인 계획 B: 텔레그램 `/renew`와 폰에서 하는 로그인

작성일 2026-11-06. 2026-11-12에 시험 결과(재발급 없음)를 보고 이 계획으로 정했고, 코드와 배포 설정을 만들었다. 남은 일은 아래 '진행 상태'에 있다.

## 배경

- 밴드 세션은 로그인 후 48시간이면 끝난다(`band_session`과 `rt`의 JWT `exp`). 로그인 유지 옵션은 휴대폰 번호 로그인에 없고, 네이버 로그인에서는 수명을 바꾸지 못했다.
- 지금은 이 PC에서 `make band-renew`(`band-login` 뒤에 `band-secret`)를 실행해야 한다. 정기 수집은 이틀에 한 번 사람이 개입한다.
- 이 계획은 연장이 안 될 때, 텔레그램에서 `/renew`를 보내면 폰에서 로그인을 끝내고 운영 세션까지 갱신되게 한다.

## 먼저 볼 것: 만료 직전 재발급 시험 결과로 하는 판단

시험: 11/9 01:34 KST에 로그인한 운영 세션(비밀 `band-state` 버전 7, 토큰 `exp` 11/11 01:34 KST)을 배치 Job이 `batch probe`로 연다. Cloud Scheduler가 11/10 07:30 KST에 한 번 실행하고, 명령이 세션을 임시 파일로 복사해서 만료 18시간 전부터 만료 5분 뒤까지 9번 밴드 홈을 열어 `exp`가 바뀌는지 Job 로그에 남긴다. 그 세션을 마지막으로 쓰는 운영 배치(11/10 03:00) 뒤에 시작하므로 시험 때문에 토큰이 바뀌어도 배치에는 영향이 없다. 운영 비밀에는 쓰지 않는다.

| 결과 | 판단 |
| --- | --- |
| `exp`가 늘었다 | 이 계획을 하지 않는다. 배치가 끝날 때 세션을 Secret Manager에 다시 저장하게 고친다. |
| 늘지 않았다 | 이 계획을 진행한다. PC가 꺼져 있는 일이 잦지 않다면 가상 화면과 noVNC로 PC를 쓰는 계획 A가 더 쉽다. |

결과: **늘지 않았다.** 실행 `abc-batch-bjr4b`가 11/10 07:34부터 11/11 01:39까지 9번 열었고, 만료 2분 전(01:31)까지 `band_session`과 `rt`의 `exp`가 11/11 01:34:26 그대로였다. 만료 5분 뒤에는 소개 페이지로 옮겨 갔다. 그 세션을 쓴 11/11, 11/12 03:00 배치는 `ErrSessionExpired`로 실패했다. PC를 늘 켜 둘 수 없어서 계획 A 대신 이 계획으로 간다.

## 진행 상태

| 단계 | 상태 |
| --- | --- |
| 1. 가능성 확인 | 일부. 이 PC(한국 IP)의 headless shell에서 로그인 화면과 휴대폰 번호 화면이 정상으로 뜨고, 화면 중계로 누르기와 글자 입력이 된다. 서울 Cloud Run IP에서 문자 인증까지 되는지는 첫 배포 뒤 실제로 로그인해 봐야 안다 |
| 2. 화면 중계 | 완료. `band.LoginScreen`(`internal/band/screen.go`), `internal/bandlogin`, `cmd/bandlogin`. `make bandlogin-local`로 이 PC에서 시험한다 |
| 3. 세션 올리기 | 완료. `internal/secretmanager`(REST, 메타데이터 서버 토큰) |
| 4. 텔레그램 | 코드 완료(`internal/telegram`, `cmd/batch/alert.go`). 봇 만들기와 웹훅 등록이 남았다 |
| 5. 배포 | 스크립트 완료(`Dockerfile.bandlogin`, `scripts/deploy.sh bandlogin`, `scripts/telegram-webhook.sh`, CI). 실제 배포가 남았다 |

운영자가 할 일(순서대로):

1. 텔레그램 BotFather에서 봇을 만들고 토큰을 받는다. 봇에게 아무 글이나 보낸 뒤 `https://api.telegram.org/bot<토큰>/getUpdates`의 `message.chat.id`로 대화방 번호를 알아낸다(웹훅을 등록하기 전에만 된다).
2. `scripts/gcp-env.sh`의 `TELEGRAM_CHAT_ID`에 번호를 넣는다.
3. `.envrc`를 읽은 셸에서 `TELEGRAM_BOT_TOKEN=<토큰> make gcp-setup`(서비스 계정, 비밀, 권한).
4. `make deploy-bandlogin`(처음 배포라서 공개 접근을 설정한다), `make telegram-webhook`, `make deploy-batch`(배치에 만료 알림 연결).
5. 텔레그램에서 `/renew`를 보내 폰으로 로그인해 본다. 문자 인증이 막히면 위험 표의 대응을 보고, 그래도 안 되면 계획 A로 간다.
6. 다 되면 바꾼 `scripts/gcp-env.sh`를 커밋한다. 일회성 스케줄러 `abc-batch-probe`가 남아 있으면 지운다(`gcloud scheduler jobs delete abc-batch-probe --location asia-northeast3`).

## 목표와 범위

- 텔레그램에서 `/renew`를 보내면 폰에서 열 수 있는 1회용 링크가 온다.
- 링크 화면은 서울에서 도는 브라우저를 비춘다. 사람이 폰으로 밴드 로그인(전화번호와 문자 인증)을 직접 한다.
- 로그인이 끝나면 서버가 세션을 Secret Manager `band-state`에 새 버전으로 올리고 이전 버전을 끈 뒤 텔레그램으로 알려 준다.
- 하지 않는 것: 비밀번호와 전화번호를 서버에 저장하거나 자동 입력하기. 로그인 화면을 봇이 대신 조작하기. 로그인 이외의 밴드 쓰기.

## 구성

새 Cloud Run 서비스 `abc-band-login`(서울 `asia-northeast3`)과 진입점 `cmd/bandlogin`을 둔다. 웹 서비스(싱가포르)와 분리하는 이유는 밴드가 한국 IP에서 로그인을 보게 하고, 로그인 중에만 CPU를 쓰며, 권한을 따로 좁히기 위해서다.

```
텔레그램 ─ 웹훅 ─> abc-band-login ──(Playwright)──> band.us
폰 브라우저 <─ 화면 프레임(SSE) / 터치·키 입력(POST) ─> abc-band-login
abc-band-login ─ 새 버전 ─> Secret Manager band-state (배치 Job이 latest를 읽는다)
```

### 서비스

- 이미지는 `Dockerfile.batch`처럼 Chromium headless shell만 넣는다(`band.InstallHeadlessBrowser`).
- Cloud Run 설정: 최소 인스턴스 0, 최대 1, 동시성 10(화면 스트림이 연결 하나를 계속 쓰므로 1이면 입력이 막힌다), 요청 제한 시간 15분, CPU를 요청이 없어도 쓰는 설정(`--no-cpu-throttling`), 메모리 2GiB. 로그인을 하는 몇 분 동안만 켜지고 평소 비용은 거의 없다.
- 서비스 계정 `abc-band-login`을 새로 만든다. 권한은 `band-state` 비밀에 `secretVersionAdder`와 `secretVersionManager`, 텔레그램 비밀 두 개(`telegram-bot-token`, `telegram-webhook-secret`)에 `secretAccessor`뿐이다.

### 경로

| 경로 | 하는 일 |
| --- | --- |
| `POST /telegram` | 텔레그램 웹훅. `X-Telegram-Bot-Api-Secret-Token`과 허용한 chat id(`TELEGRAM_CHAT_ID`)를 확인하고 `/renew`면 로그인 세션을 만들어 링크를 답한다 |
| `GET /login` | 폰에서 여는 화면. img와 숨은 입력창, 지우기·Tab·Enter 단추를 그린다. 링크는 `/login#{token}`이다 |
| `POST /login/claim` | 화면의 스크립트가 `#` 뒤의 토큰을 보내면 이 기기를 쿠키(`bl_device`)로 묶는다 |
| `GET /login/frames` | 브라우저 화면을 JPEG 프레임으로 보내는 SSE. 끝나면 `done` 이벤트로 결과를 보낸다 |
| `POST /login/input` | 누른 자리(화면 비율), 문자 입력, 키(Backspace, Enter, Tab)를 브라우저에 전달 |
| `GET /health` | 상태 확인(`/healthz`는 Google Frontend가 가로채므로 쓰지 않는다) |

화면 중계는 표준 라이브러리만 쓴다(SSE와 POST). 웹소켓 모듈을 들이지 않는다.

처음 계획은 웹훅 비밀과 링크 토큰을 경로에 넣었지만 Cloud Run 요청 로그에 경로가 남는다. 그래서 웹훅은 비밀 토큰 헤더로만 확인하고, 링크 토큰은 서버로 가지 않는 `#` 뒤에 둔다. 토큰을 받은 뒤의 요청은 기기 쿠키로만 확인한다.

### 브라우저와 화면 중계

- 폰 크기(390x844, 한국어와 `Asia/Seoul`)의 컨텍스트를 새로 만들고 로그인 수단을 고르는 화면(`auth.band.us/login_page`)을 연다. 사용자 에이전트는 `HeadlessChrome`을 뺀 데스크톱 Chrome 문자열이다. 모바일 에이전트로 바꾸면 배치(데스크톱)와 다른 기기로 보일 수 있어서 그대로 둔다.
- 프레임은 CDP `Page.startScreencast`(playwright-go의 `NewCDPSession`)로 받는다. 쓸 수 없으면 `page.Screenshot`을 0.5초 간격으로 찍는 방식으로 바꾼다.
- 입력은 `page.Mouse().Click`(좌표를 뷰포트 비율로 환산), 문자는 `page.Keyboard().InsertText`, 특수 키는 `Keyboard().Press`로 보낸다. 폰의 화상 키보드는 숨은 `<input>`에 포커스를 줘서 띄운다. 화상 키보드의 지우기가 이벤트를 주지 않는 경우를 대비해 화면 아래에 지우기, Tab, Enter 단추를 둔다.
- 로그인 감지는 지금의 `band.loggedIn`을 그대로 쓴다. 감지되면 `StorageState`를 메모리나 임시 파일(`0600`)로 받아서 올리고 파일을 지운다.

### 로그인 세션 관리

- 토큰은 `crypto/rand` 32바이트를 base64url로 만든 값이다. 인스턴스 메모리에만 두고 유효 시간 10분에 1회용(화면을 처음 연 기기에 묶는다)이다. 이미 로그인 세션이 하나 있으면 `/renew`는 새로 만들지 않고 같은 링크를 다시 답한다.
- 시간이 지나거나 완료되면 브라우저를 닫고 토큰을 지운다. 완료 전에는 Secret Manager에 아무것도 쓰지 않는다. 그래서 중간에 그만두면 지금의 세션이 그대로 남는다.
- Secret Manager 쓰기는 REST(`secretmanager.googleapis.com`)를 표준 라이브러리로 부르고, 토큰은 메타데이터 서버에서 받는다. 모듈은 더하지 않는다. 새 버전을 올린 뒤 `scripts/band-secret.sh`처럼 이전 버전을 끈다.

## 개인 정보와 보안

- 폰에서 입력하는 전화번호와 문자 인증 번호는 서버를 지나가기만 한다. 로그, 오류 메시지, 텔레그램 메시지에 입력 내용과 화면 이미지를 남기지 않는다. 프레임은 디스크에 쓰지 않는다.
- 세션 파일과 쿠키 값은 로그와 출력에 남기지 않는다. 완료 알림에는 `band.SessionLifetimes`의 만료 시각만 담는다.
- 이 링크는 밴드 계정을 원격으로 조작하는 창구다. 막는 장치는 다음과 같다.
  - 텔레그램 웹훅은 비밀 경로와 비밀 토큰 헤더, 허용 chat id를 모두 맞아야 한다.
  - 링크 토큰은 무작위 32바이트, 10분, 1회, 기기 고정이다. 같은 기기의 새로 고침은 쿠키로 다시 들어온다.
  - 링크는 텔레그램 메시지로만 전달하고 로그에 쓰지 않는다. 틀린 토큰이나 쿠키에는 응답을 똑같이 404로 주고, 1분에 20번이 넘으면 그 1분 동안은 맞는 값도 받지 않는다.
  - 다른 사이트에서 온 POST는 `http.CrossOriginProtection`으로 막는다.
  - 서비스 계정 권한은 위의 비밀들로만 좁힌다.
- 운영자 본인의 계정 로그인이고 사이트 이용자의 개인정보를 새로 받지 않는다. 텔레그램에는 운영자에게 보내는 링크와 결과 알림만 가고 밴드 글이나 회원 정보는 가지 않으므로 `privacy.html`은 바꾸지 않았다.
- `AGENTS.md`의 '밴드 수집' 규칙을 '로그인 화면은 사람이 직접 조작하고, 코드는 그 화면을 비출 뿐 자격 증명을 입력하거나 저장하지 않는다'는 뜻으로 고쳤다.

## 위험과 확인할 것

| 위험 | 대응 |
| --- | --- |
| 밴드가 헤드리스 브라우저나 새 기기를 의심해 추가 인증을 요구한다 | 만들기 전에 서울 Cloud Run에서 시험용 컨테이너를 띄워 로그인 화면이 정상으로 뜨는지 먼저 본다. 이 시험이 실패하면 계획 A로 돌아간다 |
| 헤드리스 shell의 사용자 에이전트가 `HeadlessChrome`으로 보인다 | 일반 Chrome 문자열로 바꾼다(`UserAgent` 옵션). 그래도 막히면 전체 Chromium을 이미지에 넣는다 |
| 프레임이 느려서 폰에서 쓰기 어렵다 | 해상도와 JPEG 품질을 낮추고 0.3~0.5초 간격으로 제한한다 |
| 로그인 중 인스턴스가 내려가거나 재시작된다 | 토큰과 브라우저가 메모리에만 있으므로 `/renew`를 다시 보내면 된다. 최대 인스턴스는 1이다 |
| 로그인한 세션이 밴드가 새 위치(서울 Cloud Run IP)로 보고 배치와 충돌한다 | 배치도 같은 서울 리전에서 돈다. 로그인 서비스와 배치의 IP 대역이 다르면 첫 배치 결과를 확인한다 |
| 텔레그램 웹훅을 위조해서 링크를 받아 간다 | 비밀 토큰 헤더와 chat id를 모두 맞아야 하고, 링크는 허용한 대화방으로만 간다 |

## 구현 단계

1. **가능성 확인 (0.5일).** 서울 Cloud Run에 Chromium을 띄우는 시험 컨테이너로 밴드 로그인 화면이 뜨고 전화번호 인증이 되는지 확인한다. 막히면 여기서 멈추고 계획 A로 간다.
2. **화면 중계 (1일).** `cmd/bandlogin`의 로그인 세션, 프레임 SSE, 입력 POST, 폰 화면. 먼저 로컬에서 실제 밴드 대신 `testdata/`의 가상 페이지로 시험한다.
3. **세션 올리기 (0.5일).** 로그인 감지, `StorageState`, Secret Manager 새 버전과 이전 버전 끄기. `band.SessionLifetimes`로 만료 시각을 알린다.
4. **텔레그램 (0.5일).** 봇 만들기(BotFather), 웹훅 등록, `/renew`, 완료와 실패 알림. 같은 봇으로 '세션이 24시간 안에 만료됩니다' 알림도 붙인다.
5. **배포 (0.5일).** `scripts/gcp-env.sh`와 `scripts/gcp-setup.sh`에 서비스, 서비스 계정, 비밀 이름을 더하고 `scripts/deploy.sh`와 `.github/workflows/ci.yml`에 서비스를 추가한다. 문서(`docs/spec.md`, `docs/architecture.md`, `AGENTS.md`)를 고친다.

모두 합쳐 3일 안팎이다. 1단계 결과로 계속할지 다시 정한다.

## 테스트

- 외부(Telegram, Secret Manager, 밴드)는 인터페이스 뒤에 두고 가짜 구현으로 단위 테스트한다. 기본 테스트에서 외부 서비스를 부르지 않는다.
- 실패와 경계를 함께 본다.
  - 허용하지 않은 chat id, 틀린 웹훅 비밀, 틀린 토큰.
  - 만료된 토큰, 이미 쓴 토큰, 다른 기기에서 온 요청.
  - 로그인을 끝내지 않고 시간 초과: Secret Manager에 쓰지 않고 브라우저를 닫는지.
  - 로그인 직후 `StorageState` 저장 실패, Secret Manager 쓰기 실패: 이전 버전을 끄지 않는지.
  - 로그와 텔레그램 메시지에 입력 내용과 쿠키 값이 없는지.
- 실제 밴드와 텔레그램을 쓰는 시험은 `integration` 태그를 붙이고 `make test-integration`으로만 돌린다.

## 되돌리기

서비스와 서비스 계정, 텔레그램 비밀, 웹훅 등록을 지우면 원래대로 `make band-renew`만 남는다. 배치와 웹은 이 서비스에 의존하지 않는다.

## 열린 질문

- 텔레그램 봇은 새로 만든다(이미 있는지 미확인). 허용할 chat id는 `/start`로 알아낸다.
- 링크를 여는 폰이 텔레그램 안 브라우저일 때 문자 입력과 키보드가 잘 되는지.
- 완료 뒤 곧바로 배치를 한 번 돌릴지(`gcloud run jobs execute`는 서비스 계정에 권한이 더 필요하다). 일단은 다음 정기 실행을 기다린다.
- 텔레그램 안 브라우저에서 한글 입력은 시험하지 않았다. 휴대폰 로그인은 숫자만 넣으므로 문제가 되면 외부 브라우저로 연다.
