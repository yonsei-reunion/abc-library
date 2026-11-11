.PHONY: fmt lint test check ci test-integration \
	db-up db-seed db-psql db-down db-reset web batch-local \
	web-prod migrate migrate-unlock band-login band-renew batch status \
	gcp-setup gcp-schedule band-secret deploy-web deploy-batch deploy-bandlogin telegram-webhook bandlogin-local firebase-hosting release

# 로컬 PostgreSQL(compose.yaml의 db) 연결. 이 컴퓨터에서만 쓰는 개발용 값이라 비밀이 아니다.
# .envrc의 DB_URL은 운영 DB(Neon)를 가리키고 배포 스크립트도 그 값을 쓰므로, 로컬 DB는 명령마다 이 값으로 바꿔 쓴다.
LOCAL_DB = DB_URL='postgres://abc@localhost:5433/abc_library?sslmode=disable' DB_PWD=abc

fmt:
	goimports -w .

lint:
	golangci-lint run ./...

test:
	go test ./...

check: fmt lint test

ci: lint test

# 로컬 DB(테스트마다 임시 스키마)와 실제 Claude API를 호출한다. CLAUDE_API_KEY는 .envrc에서 읽는다.
test-integration: db-up
	$(LOCAL_DB) go test -tags integration -count=1 ./...

# 로컬 개발: compose.yaml의 PostgreSQL을 쓴다.
# 처음 한 번: make db-seed → make web

# 로컬 DB를 띄우고 마이그레이션을 적용한다.
db-up:
	docker compose up -d --wait db
	$(LOCAL_DB) go run ./cmd/batch migrate

# 가상의 데모 데이터를 넣는다. 관리 화면을 쓰려면 make db-seed ARGS="-admin <Google 계정 메일 주소>"
db-seed: db-up
	$(LOCAL_DB) go run ./cmd/batch seed $(ARGS)

db-psql:
	docker compose exec db psql -U abc -d abc_library

db-down:
	docker compose down

# 로컬 DB의 데이터를 모두 지우고 데모 데이터로 다시 시작한다.
db-reset:
	docker compose down -v
	$(MAKE) db-seed

web: db-up
	$(LOCAL_DB) go run ./cmd/web

# 로컬 DB로 배치를 돌린다. 밴드와 Claude API는 실제로 호출한다. 예: make batch-local ARGS="-limit 3"
batch-local: db-up
	$(LOCAL_DB) go run ./cmd/batch run $(ARGS)

# 운영: .envrc의 운영 DB(Neon)를 쓴다.

# 로컬에서 고친 화면을 운영 데이터로 확인한다. 관리 화면에서 고친 내용은 운영 DB에 저장된다.
web-prod:
	go run ./cmd/web

# 스키마를 바꾼 코드를 release에 올리기 전에 운영 DB에 먼저 적용한다. make release가 필요할 때 대신 실행한다.
migrate:
	go run ./cmd/batch migrate

# 마이그레이션 중에 프로세스가 강제로 끝나서 남은 잠금(migrations table is already locked)을 운영 DB에서 푼다.
migrate-unlock:
	go run ./cmd/batch unlock

band-login:
	go run ./cmd/batch login

# 예: make batch ARGS="-limit 0 -redo-from 1", make batch ARGS="-recheck-days 0 -redo-posts 175,184"
batch:
	go run ./cmd/batch run $(ARGS)

status:
	go run ./cmd/batch status

# Google Cloud 배포. 값은 scripts/gcp-env.sh에 있다.
# 처음 한 번: make gcp-setup → make band-secret → make deploy-web deploy-batch → make gcp-schedule
gcp-setup:
	scripts/gcp-setup.sh

gcp-schedule:
	scripts/gcp-schedule.sh

# 밴드 세션이 만료되면 make band-login 뒤에 실행한다.
band-secret:
	scripts/band-secret.sh

# 밴드 세션이 만료되기 전이나 후에 이것 하나만 실행한다. 로그인(창에서 직접)과 비밀 갱신을 이어서 한다.
# 로그인이 실패하거나 시간이 지나면 예전 세션 파일을 올리지 않도록 거기서 멈춘다.
band-renew: band-login band-secret

# 폰에서 하는 다시 로그인(cmd/bandlogin)을 이 컴퓨터에서 시험한다. 출력한 링크를 브라우저에서 열고 로그인하면
# 세션을 band-state.json에 저장하고 끝난다. 운영 비밀은 바꾸지 않으므로 이어서 make band-secret을 실행한다.
bandlogin-local:
	go run ./cmd/bandlogin -local

deploy-web:
	scripts/deploy.sh web

deploy-batch:
	scripts/deploy.sh batch

deploy-bandlogin:
	scripts/deploy.sh bandlogin

# bandlogin 서비스를 텔레그램 봇의 웹훅으로 등록한다. 처음 배포한 뒤에 한 번 실행한다.
telegram-webhook:
	scripts/telegram-webhook.sh

# origin/main을 release로 옮겨서 GitHub Actions가 배포하게 한다. 마이그레이션이 바뀌었으면 운영 DB에 먼저 적용한다.
# 특정 커밋까지만 배포하려면 make release ARGS=<커밋>
release:
	scripts/release.sh $(ARGS)

# https://abc-lib.web.app을 웹 서비스에 연결한다. rewrite 설정을 바꿀 때만 다시 실행한다.
firebase-hosting:
	scripts/firebase-hosting.sh
