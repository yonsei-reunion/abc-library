# abc-library 개발 지침

아카라카북클럽(ABC) 밴드에 올라온 글에서 책을 추출해서 책, 저자, 장르별로 보여 주는 웹사이트다.
하루 한 번 배치가 밴드 글과 댓글을 수집하고 Claude로 책 정보를 추출해서 DB에 반영한다.
매주 월요일에는 카카오톡 ABC 대화방의 지난주 대화를 Claude로 정리한 '대화방 주간 요약'을 밴드에 올린다.
화면, 배치 흐름, 데이터 모델, 운영 환경은 `docs/spec.md`를 따른다. 인프라 구성 그림은 `docs/architecture.md`에 있다.

## 기술 스택

- 서버는 Go 표준 라이브러리(`net/http`, `html/template`)로 만들고 화면의 상호작용은 HTMX로 처리한다. SPA 프레임워크나 JS 빌드 도구는 들이지 않는다.
- DB는 Neon(PostgreSQL)이고 Bun으로 접근한다. 스키마 변경은 `internal/store/migrations`의 Bun 마이그레이션 파일로만 한다.
- 마이그레이션 파일은 `.tx.up.sql`과 `.tx.down.sql`로 만들어서 한 파일을 한 트랜잭션에서 실행한다. 적용 기록은 성공한 뒤에만 남는다. 트랜잭션 안에서 못 하는 문장(`CREATE INDEX CONCURRENTLY` 등)이 든 파일만 `.up.sql`로 두고, 실패해도 다시 실행할 수 있게 `IF NOT EXISTS`처럼 쓴다.
- 밴드 수집은 playwright-go(`github.com/mxschmitt/playwright-go`)로 한다.
- 책 정보 추출은 공식 Go SDK(`github.com/anthropics/anthropic-sdk-go`)로 Claude를 호출한다. 모델은 `claude-opus-5-5`, effort는 `medium`으로 명시한다.
- 의존성은 최소로 유지한다. 표준 라이브러리로 하기 어려울 때만 모듈을 추가한다.

## 구조

- 진입점은 `cmd/web`(웹 서버), `cmd/batch`(일일 배치), `cmd/bandlogin`(밴드 다시 로그인)이고, 나머지 코드는 `internal/` 아래에 둔다.
- `internal/book`(도메인)은 밴드, Claude, DB, HTTP 패키지에 의존하지 않는다. 의존 방향은 바깥 계층에서 도메인 쪽이다.
- 설정 속성은 환경 변수로 받는다. 로컬에서는 `.envrc`(커밋하지 않음)에 두고, 속성 목록은 `.envrc.example`에 적는다.
- DB 연결은 `DB_URL`(비밀번호를 뺀 URL)과 `DB_PWD`를 조합한다.
- `.envrc`의 `DB_URL`은 운영 DB(Neon)를 가리킨다. 로컬 개발(`make web`, `make test-integration`, `make db-*`, `make batch-local`)은 `compose.yaml`의 PostgreSQL을 쓰고, Makefile의 `LOCAL_DB`로 명령마다 연결을 바꾼다. 운영 DB를 쓰는 명령은 `make migrate`, `make batch`, `make status`, `make web-prod`다. 배포 스크립트가 `.envrc`의 `DB_URL`을 운영에 넣으므로 `.envrc`를 로컬 DB로 바꾸지 않는다.

## 개인 정보

- 밴드는 비공개다. 사이트에는 게시글과 댓글의 원문 대신 Claude가 개인 정보를 뺀 언급 요약만 싣는다.
- 게시글과 댓글의 작성자 이름과 원문은 DB에 저장하지 않고 화면, 로그, 출력에도 남기지 않는다.
- 관리자의 메일 주소는 `admins`에만 두고 로그에 남기지 않는다.
- 북마크를 쓰는 사용자의 메일 주소는 받지도 저장하지도 않는다. 사용자 로그인은 Google에 `openid`만 요청하고, 사용자는 Google 계정 고유 번호(sub)를 `USER_ID_SECRET`으로 HMAC-SHA256한 값(`users.subject_hash`)으로만 구별한다. 메일 주소를 받는 것은 관리자 로그인뿐이다.
- 사용자 테이블에 이름, 메일 주소, 프로필 같은 정보를 더하지 않는다. 로그인 화면(`/login`)의 저장 정보 안내를 실제 동작과 맞춘다.
- 도움말(`/help`)의 화면 캡처(`internal/web/static/help/`)는 내 북마크 화면 하나이고, 가상의 데모 데이터로 찍는다. 실제 밴드 요약이 담긴 화면을 넣지 않는다.
- 처리하는 개인정보, 보유 기간, 외부 서비스(위탁과 국외 이전), 쿠키가 바뀌면 개인정보 처리방침(`internal/web/templates/privacy.html`)도 함께 고치고 시행일을 바꾼다.
- 관리자는 운영자 본인뿐이라서 처리방침에 관리자 메일 주소를 적지 않았다. 운영자 말고 다른 관리자를 더하게 되면 처리방침에 관리자 메일 주소의 항목, 목적, 보유 기간을 다시 넣는다.

## 밴드 수집

- 로그인 화면은 사람이 직접 조작하고, 코드는 저장된 Playwright `storageState`만 쓴다. 비밀번호를 코드, 설정, 로그에 두지 않는다.
- 세션은 로그인 후 48시간에 끝나고 쓰는 동안 다시 발급되지 않는다. 다시 로그인은 이 컴퓨터의 `make band-renew`나 폰의 텔레그램 `/renew`(`cmd/bandlogin`)로 한다. bandlogin은 로그인 화면을 폰에 비추고 받은 입력을 전달할 뿐이고, 자격 증명을 입력하거나 저장하지 않는다. 입력 내용, 화면 이미지, 링크 토큰을 로그와 메시지에 남기지 않는다.
- 세션 파일과 쿠키 값은 커밋하지 않고 로그나 출력에도 남기지 않는다.
- 웹 클라이언트가 그린 화면을 읽기만 한다. 밴드 내부 API 요청을 HTTP로 직접 재현하거나 앱을 역분석하지 않는다.
- 수집은 읽기만 한다. 접힌 이전 댓글을 펼치거나 게시글 목록을 '최신 댓글순'으로 바꾸는 것처럼 읽기 위한 동작만 한다(정렬은 계정 설정에 저장되지 않는다). 수집은 하루 한 번으로 제한한다.
- 자동으로 하는 쓰기 동작은 주간 배치(`batch weekly`)가 일주일에 한 번 대화방 주간 요약 글을 웹 화면에서 올리는 것 하나뿐이다(`internal/band/write.go`). ABC 밴드는 게시판을 골라야 글이 올라가므로 `BAND_BOARD`(기본 `자유게시판`)를 고른다. 댓글, 표정, 글쓰기 설정 변경 같은 다른 쓰기는 자동화하지 않는다.
- 같은 기간의 요약을 두 번 올리지 않도록, 올리기 전에 게시글 목록에서 요약의 기간 줄을 찾는다. 개발 중 시험 게시는 ABC 밴드가 아닌 시험용 밴드에 `-band-no`로 한다.

## 대화방 주간 요약

- 카카오톡 ABC 대화방 메시지는 운영자의 폰에서 kakao-to-google-sheet 앱이 모아 Google 스프레드시트에 쌓는다. 배치는 시트에 붙은 Apps Script 웹 앱(`KAKAO_SHEET_URL`, `KAKAO_SHEET_TOKEN`)으로 읽기만 한다(`internal/kakao`).
- 메시지 원문과 보낸 사람은 DB에 저장하지 않고 로그와 출력에도 남기지 않는다. Claude에게는 본문과 보낸 시각만 보내고 보낸 사람 이름은 보내지 않는다.
- 링크는 코드가 본문에서 모두 뽑아서 책이나 링크 모음에 하나씩 넣는다. Claude는 링크를 책에 붙이거나 설명만 한다. 책의 구매 링크는 교보문고로 통일하고, ISBN과 상품 번호는 일일 배치와 같이 검색 결과와 상품 링크에 있던 것만 받아들인다(`internal/weekly`).
- 글은 밴드에 붙여 넣는 형식(제목 줄과 하이픈 목록, 이모지 없음)으로 코드가 만든다. 메시지가 없는 주는 올리지 않는다.

## 교보문고

- 구매 링크와 표지 이미지는 교보문고로 통일한다. 표지는 바코드로 교보문고 이미지 주소를 만들어 쓴다. 일반 도서는 ISBN이 바코드이고, 잡지나 전자책처럼 바코드가 ISBN과 다른 상품은 `books.kyobo_barcode`를 먼저 쓴다(`book.Book.CoverCode`). 전자책은 ISBN이 있어도 전자책 바코드로만 표지를 준다. 상품 번호로는 표지를 주지 않는다.
- 교보문고 검색은 새 책을 확정할 때만 호출하고, 결과는 DB에 저장해서 다시 묻지 않는다. robots.txt가 막은 `/api/` 경로는 호출하지 않는다.
- 글에 교보문고 상품 링크(`product.kyobobook.co.kr/detail/{상품 번호}`, 전자책은 `ebook-product.kyobobook.co.kr/dig/epd/ebook/{상품 번호}`)가 있으면 코드가 상품 번호로 검색해서(`kyobo.Client.Product`) 그 상품의 판본을 찾고, 링크 옆에 붙여 Claude에게 보여 준다. ISBN이 없는 상품도 받아들이고, Claude는 응답의 `kyobo_pid`로 그 상품을 고른다. 상품 페이지는 봇 요청에 빈 응답을 주므로 읽지 않는다.
- 관리 화면에서 ISBN 없이 상품 번호만 넣거나 전자책 상품 번호(`E…`)를 넣고 저장하면 웹 서버가 상품 번호로 검색해서 바코드를 채운다.
- 검색 결과 HTML은 저자 이름과 역할 사이에 주석이 끼어 있다. `testdata/`도 실제 페이지처럼 주석을 남겨 둔다.

## 언급과 링크

- 언급은 게시글 본문이나 댓글 하나가 책 한 권에 대해 한 이야기다. 책 상세에는 게시글별로 본문 언급 아래에 댓글 언급을 답글처럼 붙인다.
- 댓글이 본문의 책에 대한 것인지 다른 책에 대한 것인지는 Claude가 판정한다. 코드에서 제목 매칭 같은 규칙으로 추측하지 않는다.
- 언급에 들어 있던 링크는 코드가 모두 붙이고, 서점 구매 링크(`extract.IsStoreLink`)와 Claude가 그 책과 직접 관련이 없다고 판정한 링크(`unrelated_links`)만 뺀다. Claude에게 붙일 링크를 고르게 하지 않고 뺄 링크를 판정하게 한다. 고르게 하면 링크가 빠진다.
- 회원의 개인 블로그나 SNS 링크도 언급에 있으면 붙인다. 모임에서 정한 것이므로 개인 정보라는 이유로 빼지 않는다. 요약 본문에서 개인 정보를 빼는 규칙과는 별개다.

## 태그

- 책 한 권에 태그를 여러 개 단다(`book_tags`). 태그 목록은 `tags` 테이블에 있고 관리 화면(`/admin/tags`)에서 더하고, 이름을 바꾸고, 지운다. 책 편집 화면(`/admin/books/{id}`)에서도 목록에 없는 태그를 더하면서 붙인다. 코드에 태그 목록을 두지 않는다.
- 태그마다 모임 주제인지(`book.Tag.Topic`, `tags.topic`)만 정한다. 모임 주제가 아닌 태그는 소설, 인문 같은 책의 갈래다. 관리 화면은 태그를 갈래로 나누지 않고 한 목록에 두고, 모임 주제인지를 체크 상자로 고친다. 배치는 실행할 때 DB의 목록(`book.TagList`)을 읽어서 그 안에서만 고르게 한다.
- 새 모임 주제가 정해지면 관리 화면에서 모임 주제를 체크해서 더하고(맨 앞에 붙는다), `make batch ARGS=-redo-from=1`로 저장한 글을 다시 추출한다.

## 북마크

- 북마크(`bookmarks`)는 사용자와 책을 잇고, 사용자의 카테고리(`bookmark_categories`) 여러 개에 넣을 수 있다(`bookmark_category_books`). 다른 사용자의 카테고리와 이어지지 않게 외래 키에 `user_id`를 함께 둔다.
- 사용자 화면의 조회와 수정은 모두 로그인한 사용자의 번호로 거른다. 다른 사용자의 카테고리는 없는 것(404)으로 다룬다.
- 카테고리 본문은 Markdown이고 goldmark의 안전한 기본값으로 그린다. `html.WithUnsafe`를 쓰지 않는다.
- 공개 주소는 `/shared/{code}`이고, `code`는 `book.NewCode`가 임의로 뽑은 영어 소문자 3자다. 겹치는 일이 잦아지면 한 글자씩 늘린다. 번호를 바꿔서 만들지 않는다. 공개한 카테고리는 '공개 북마크'(`/shared`)에 모두 실린다.
- 배치는 북마크한 책을 지우지 않고, 책을 합치면 북마크도 옮긴다.

## 관리자

- 관리 화면(`/admin/…`)은 Google 계정으로 로그인한 관리자만 쓴다. 로그인은 `internal/auth`가 표준 라이브러리로 OpenID Connect를 처리한다. 관리자 목록은 `admins` 테이블에 있고 관리 화면에서 더하고 뺀다.
- 세션 쿠키 이름은 `__session`만 쓴다. Firebase Hosting이 Cloud Run으로 넘길 때 다른 쿠키를 지운다. 로그인 중의 state도 이 쿠키에 담는다.
- 관리자인지는 요청마다 DB에서 확인한다. 관리 화면의 POST는 `http.CrossOriginProtection`으로 다른 사이트에서 온 요청을 막는다.
- 관리자가 고친 책(`books.edited_at`)의 정보와 태그, 고친 언급(`mentions.edited`)은 배치가 다시 추출해도 바꾸지 않는다. 지운 언급은 `hidden`으로 남겨서 배치가 되살리지 않게 한다. 화면의 조회는 지운 언급을 빼야 한다(`NOT m.hidden`).

## 책 식별

- 책은 ISBN-13으로 식별하고, 같은 작품의 다른 판본이나 ISBN이 없는 책은 `book.WorkKey`(정규화한 제목과 대표 저자)로 묶는다.
- 배치가 같은 책을 찾는 키는 `book_keys`에 둔다(작품 키와 `isbn:`을 붙인 ISBN). 관리자가 제목이나 ISBN을 고치거나 책을 합쳐도 예전 키를 지우지 않는다.
- ISBN은 Claude가 `search_kyobo` 도구 결과와 상품 링크로 찾은 판본에서 고르고, 코드는 거기에 실제로 있었던 ISBN만 받아들인다. 링크의 상품이 가리키는 책이면 그 판본을 먼저 고르고, 상품 번호도 링크의 것을 쓴다.
- 글쓴이가 상품 링크로 가리킨 판본(`book.Finding.Linked`)은 작품 키가 같아도 ISBN이 다른 책과 묶지 않는다. 괄호 속 표기를 빼면 제목이 같은 다른 책이 있다(예: 시간의 역사와 시간의 역사(결정판)).
- 여러 권으로 된 책(시리즈, 상·하권)은 언급 하나에서 하나만 넣고, ISBN은 다음 순서로 고른다.
  1. 특정 권을 언급하면 그 권. 여러 권을 짚어 언급하면 그중 가장 앞선 권
  2. 특정 권 언급이 없으면 교보문고에 합본이나 세트 상품의 ISBN이 있을 때 그 ISBN
  3. 합본이나 세트 ISBN이 없으면 1권

## Claude 호출

- 인증 정보는 코드에 두지 않는다. 로컬은 `.envrc`의 `CLAUDE_API_KEY`를 쓴다. 운영 배치는 `ANTHROPIC_FEDERATION_RULE_ID` 등(`scripts/gcp-env.sh`)이 설정되어 Cloud Run 메타데이터 서버의 ID 토큰으로 Workload Identity Federation을 쓴다. 토큰은 Console의 `abc-library` 워크스페이스로 범위가 제한된다.
- `ANTHROPIC_API_KEY`라는 이름은 쓰지 않는다. 같은 디렉터리에서 실행한 Claude Code 같은 도구가 그 키를 가져다 쓰게 된다.
- Claude 구독(OAuth) 토큰은 코드에서 쓰지 않는다.
- 응답은 JSON 스키마를 지정한 structured outputs로 받는다. `stop_reason`이 `refusal`이면 그 글을 책 없이 저장하고 기록한다.

## 배포

- Google Cloud 프로젝트 이름, 리전, 서비스 계정, 비밀 이름은 `scripts/gcp-env.sh`에만 둔다. 절차는 `docs/spec.md`의 '배포'를 따른다.
- 운영 배포는 `release` 브랜치에 merge할 때만 한다. 배포는 `make release`(`scripts/release.sh`)로 하고, 이 명령이 마이그레이션이 바뀌었으면 운영 DB에 먼저 적용한다. GitHub Actions(`.github/workflows/ci.yml`)가 `release`에 push되면 `make ci`를 통과한 뒤 웹, 배치, bandlogin을 배포한다. bandlogin은 공개 접근을 설정해야 해서 처음 한 번은 `make deploy-bandlogin`으로 직접 배포한다. `main`에 push하면 검사만 한다.
- 웹, 배치, bandlogin은 배포된 이미지의 커밋 이후 각자에 들어가는 파일이 바뀌었을 때만 배포한다(`scripts/deploy.sh --if-changed`). 무엇이 들어가는지는 `go list -deps`로 계산하므로 목록을 따로 두지 않는다.
- 배치와 bandlogin 이미지에는 전체 Chromium 없이 headless shell만 넣는다. 배치에서 브라우저 설치를 확인할 때는 `band.InstallHeadlessBrowser`를 쓴다.

## 테스트

- 밴드, Claude, Neon, 교보문고를 호출하는 코드는 인터페이스 뒤에 두고, 단위 테스트는 가짜 구현과 `testdata/`의 고정 데이터로 검증한다. 기본 테스트에서는 외부 서비스를 호출하지 않는다.
- 실제 서비스를 호출하는 테스트는 `integration` 빌드 태그를 붙이고 `make test-integration`으로 실행한다. DB 테스트는 로컬 PostgreSQL에 임시 스키마를 만들어 쓰고 지운다.
- 로컬 DB의 데모 데이터(`make db-seed`, `cmd/batch/seed.go`)도 가상의 요약을 쓴다. 실제 밴드 요약을 넣지 않는다.
- `testdata/`에 실제 밴드 게시글이나 회원 정보를 넣지 않는다. 화면 구조를 흉내 낸 가상의 데이터를 쓴다.
- 정상 경로만 확인하지 말고 실패해야 하는 경우와 경계값도 함께 검증한다.

## 검증

- 검증은 훅이 한다. 파일을 고친 직후 `scripts/check-go.sh`가 포맷과 린트를, 작업을 마칠 때 `scripts/verify.sh`가 `make ci`를 실행한다. 문서(`.md` 파일과 `docs/`)만 바뀌었으면 건너뛴다.
- 포맷과 린트 규칙은 `.golangci.yml`이 정한다.

## 언어

- 문서, 주석, 화면 문구, 오류 메시지는 한국어로 쓰고 식별자는 영어로 쓴다.

## 에이전트 설정

- 이 파일이 Claude Code와 Codex의 공통 지침이다. `CLAUDE.md`는 만들지 않는다. 그 파일이 있으면 Claude Code가 이 파일을 읽지 않는다.
- 검증 로직은 `scripts/`의 셸 스크립트에 둔다. `.claude/settings.json`과 `.codex/hooks.json`의 훅은 그 스크립트를 부르기만 한다.
- 스킬의 원본은 `.agents/skills/<이름>/`에 두고, `.claude/skills/<이름>`은 원본을 가리키는 심볼릭 링크로 만든다.
- 특정 디렉터리에만 적용되는 규칙은 그 디렉터리의 `AGENTS.md`에 적는다.
