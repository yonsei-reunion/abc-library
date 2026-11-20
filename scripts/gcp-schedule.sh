#!/usr/bin/env bash
# 배치 Job을 하루 한 번 실행하는 Cloud Scheduler 작업, 월요일 아침에 대화방 주간 요약(batch weekly)으로 실행하는 작업과,
# Job이 실패하면 메일을 보내는 알림을 만든다. 텔레그램 /batch와 /weekly로 Job을 실행하는 bandlogin에도 실행 권한을 준다.
# 배치 Job을 처음 배포한 뒤에 한 번 실행한다. 이미 있으면 건너뛰거나 갱신하므로 다시 실행해도 된다.
#
#   make gcp-schedule                         알림은 gcloud에 로그인한 계정의 메일로 보낸다
#   ALERT_EMAIL=me@example.com make gcp-schedule
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=gcp-env.sh
source "$root/scripts/gcp-env.sh"

# 매일 새벽 3시(KST)
SCHEDULE="0 3 * * *"
# 매주 월요일 아침 7시(KST). 지난주 월요일부터 어제(일요일)까지의 대화를 요약한다.
WEEKLY_SCHEDULE="0 7 * * 1"
ALERT_EMAIL="${ALERT_EMAIL:-$(gcloud config get-value account 2>/dev/null)}"

echo "== 배치 Job 실행 권한 =="
gcloud run jobs add-iam-policy-binding "$BATCH_JOB" --region "$BATCH_REGION" --project "$PROJECT" \
  --member "serviceAccount:$SCHEDULER_SA" --role roles/run.invoker >/dev/null
echo "  roles/run.invoker ($BATCH_JOB → $SCHEDULER_SA)"
# 주간 요약은 같은 Job을 인자만 바꿔(overrides) 실행한다. run.invoker에는 그 권한(run.jobs.runWithOverrides)이 없다.
gcloud run jobs add-iam-policy-binding "$BATCH_JOB" --region "$BATCH_REGION" --project "$PROJECT" \
  --member "serviceAccount:$SCHEDULER_SA" --role roles/run.jobsExecutorWithOverrides >/dev/null
echo "  roles/run.jobsExecutorWithOverrides ($BATCH_JOB → $SCHEDULER_SA)"
# bandlogin은 텔레그램 /batch와 /weekly로 같은 Job을 인자를 바꿔 실행하고, 실행 중이거나 오늘 성공한 실행이 있는지
# 실행 기록으로 확인한다(run.executions.list는 run.viewer에 있다). 권한은 이 Job에만 준다.
if [[ -n "$TELEGRAM_CHAT_ID" ]]; then
  for role in roles/run.jobsExecutorWithOverrides roles/run.viewer; do
    gcloud run jobs add-iam-policy-binding "$BATCH_JOB" --region "$BATCH_REGION" --project "$PROJECT" \
      --member "serviceAccount:$LOGIN_SA" --role "$role" >/dev/null
    echo "  $role ($BATCH_JOB → $LOGIN_SA)"
  done
fi

echo "== Cloud Scheduler =="
uri="https://run.googleapis.com/v2/projects/${PROJECT}/locations/${BATCH_REGION}/jobs/${BATCH_JOB}:run"
# Job 실행 요청은 바로 끝나므로 재시도하지 않는다. 재시도하면 배치가 두 번 돌 수 있다.
flags=(--location "$BATCH_REGION" --project "$PROJECT"
  --schedule "$SCHEDULE" --time-zone Asia/Seoul
  --uri "$uri" --http-method POST
  --oauth-service-account-email "$SCHEDULER_SA"
  --max-retry-attempts 0)
if gcloud scheduler jobs describe "$SCHEDULER_JOB" --location "$BATCH_REGION" --project "$PROJECT" >/dev/null 2>&1; then
  gcloud scheduler jobs update http "$SCHEDULER_JOB" "${flags[@]}" >/dev/null
else
  gcloud scheduler jobs create http "$SCHEDULER_JOB" "${flags[@]}" >/dev/null
fi
echo "  $SCHEDULER_JOB: $SCHEDULE (Asia/Seoul)"

weekly_flags=(--location "$BATCH_REGION" --project "$PROJECT"
  --schedule "$WEEKLY_SCHEDULE" --time-zone Asia/Seoul
  --uri "$uri" --http-method POST
  --message-body '{"overrides":{"containerOverrides":[{"args":["weekly"]}]}}'
  --oauth-service-account-email "$SCHEDULER_SA"
  --max-retry-attempts 0)
if gcloud scheduler jobs describe "$WEEKLY_SCHEDULER_JOB" --location "$BATCH_REGION" --project "$PROJECT" >/dev/null 2>&1; then
  gcloud scheduler jobs update http "$WEEKLY_SCHEDULER_JOB" "${weekly_flags[@]}" \
    --update-headers "Content-Type=application/json" >/dev/null
else
  gcloud scheduler jobs create http "$WEEKLY_SCHEDULER_JOB" "${weekly_flags[@]}" \
    --headers "Content-Type=application/json" >/dev/null
fi
echo "  $WEEKLY_SCHEDULER_JOB: $WEEKLY_SCHEDULE (Asia/Seoul, batch weekly)"

echo "== 실패 알림 =="
channel_name="abc-library 알림 메일"
channel="$(gcloud beta monitoring channels list --project "$PROJECT" \
  --filter "displayName=\"$channel_name\"" --format 'value(name)' | head -n1)"
if [[ -z "$channel" ]]; then
  channel="$(gcloud beta monitoring channels create --project "$PROJECT" \
    --display-name "$channel_name" --type email \
    --channel-labels "email_address=$ALERT_EMAIL" --format 'value(name)')"
fi
echo "  알림 채널: $channel_name"

policy_name="abc-batch 실패"
if [[ -z "$(gcloud beta monitoring policies list --project "$PROJECT" \
  --filter "displayName=\"$policy_name\"" --format 'value(name)')" ]]; then
  policy="$(mktemp)"
  trap 'rm -f "$policy"' EXIT
  cat >"$policy" <<EOF
{
  "displayName": "$policy_name",
  "combiner": "OR",
  "conditions": [{
    "displayName": "$BATCH_JOB 실행 실패",
    "conditionThreshold": {
      "filter": "resource.type = \"cloud_run_job\" AND resource.labels.job_name = \"$BATCH_JOB\" AND metric.type = \"run.googleapis.com/job/completed_execution_count\" AND metric.labels.result = \"failed\"",
      "aggregations": [{"alignmentPeriod": "300s", "perSeriesAligner": "ALIGN_DELTA", "crossSeriesReducer": "REDUCE_SUM"}],
      "comparison": "COMPARISON_GT",
      "thresholdValue": 0,
      "duration": "0s",
      "trigger": {"count": 1}
    }
  }],
  "alertStrategy": {"autoClose": "86400s"},
  "notificationChannels": ["$channel"],
  "documentation": {
    "mimeType": "text/markdown",
    "content": "abc-library 일일 배치가 실패했습니다. Cloud Run Job 로그에서 원인을 확인하세요. 밴드 세션이 만료되었으면 로컬에서 make band-renew를 실행합니다."
  }
}
EOF
  gcloud beta monitoring policies create --project "$PROJECT" --policy-from-file "$policy" >/dev/null
fi
echo "  알림 정책: $policy_name → $ALERT_EMAIL"
