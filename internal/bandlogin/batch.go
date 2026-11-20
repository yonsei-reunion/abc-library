package bandlogin

import (
	"context"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/cloudrun"
)

// BatchJob은 배치 Job(abc-batch)을 실행하고 최근 실행을 읽는다. cloudrun.Job이 실제 구현이다.
type BatchJob interface {
	Executions(ctx context.Context, n int) ([]cloudrun.Execution, error)
	Run(ctx context.Context, args []string, env map[string]string) (string, error)
}

// ReportEnv는 텔레그램에서 시작한 실행에 넣는 환경 변수다. 배치는 이 값이 있으면 끝날 때 결과를 텔레그램으로 알린다.
const ReportEnv = "TELEGRAM_REPORT"

// recentExecutions는 실행 중인 실행과 오늘의 일일 배치를 찾으려고 읽는 최근 실행 수다.
// 하루에 일일 배치 한 번과 가끔 주간 요약이 돌기 때문에 이틀치보다 넉넉하다.
const recentExecutions = 20

// batchKind는 텔레그램 명령에 맞는 배치 명령이다.
var batchKind = map[string]struct {
	args []string
	// obj는 목적격 조사를 붙인 이름이다.
	obj string
}{
	"scrap":  {[]string{"run"}, "일일 배치를"},
	"weekly": {[]string{"weekly"}, "대화방 주간 요약을"},
}

// startBatch는 배치 Job을 실행하고 답할 문구를 돌려준다. 동시에 온 명령이 둘 다 실행되지 않도록 하나씩 처리한다.
func (s *Server) startBatch(ctx context.Context, cmd string) string {
	kind := batchKind[cmd]
	if s.Batch == nil {
		return "배치 실행을 설정하지 않았습니다."
	}
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	execs, err := s.Batch.Executions(ctx, recentExecutions)
	if err != nil {
		s.Log.Error("배치 실행 기록을 읽지 못했습니다", "오류", err)
		return "배치 실행 기록을 읽지 못해서 실행하지 않았습니다."
	}
	if msg := batchBlocked(cmd, execs, s.now()); msg != "" {
		return msg
	}
	name, err := s.Batch.Run(ctx, kind.args, map[string]string{ReportEnv: "1"})
	if err != nil {
		s.Log.Error("배치를 실행하지 못했습니다", "명령", cmd, "오류", err)
		return kind.obj + " 실행하지 못했습니다."
	}
	s.Log.Info("텔레그램 명령으로 배치를 실행했습니다", "명령", cmd, "실행", name)
	return kind.obj + " 시작했습니다(실행 " + name + "). 끝나면 결과를 보냅니다."
}

// batchBlocked는 지금 실행하면 안 되는 이유를 돌려준다. 실행해도 되면 빈 문자열이다.
//   - 배치가 실행 중이면 어느 명령이든 실행하지 않는다. 같은 밴드 세션과 DB를 함께 쓰기 때문이다.
//   - 일일 배치는 오늘(KST) 성공한 일일 배치가 있으면 실행하지 않는다. 밴드 수집은 하루 한 번으로 제한한다.
//     실패한 실행은 세지 않으므로 세션이 만료되어 실패한 날은 /renew 뒤에 다시 실행할 수 있다.
//   - 주간 요약은 배치가 같은 기간의 글을 밴드에서 찾아 두 번 올리지 않으므로 더 막지 않는다.
func batchBlocked(cmd string, execs []cloudrun.Execution, now time.Time) string {
	today := now.In(book.Seoul).Format("2006-01-02")
	for _, e := range execs {
		if e.Running() {
			return "실행 중인 배치가 있습니다(실행 " + e.Name + ", " + e.Created.In(book.Seoul).Format("15:04") + " 시작). 끝난 뒤에 다시 보내세요."
		}
	}
	if cmd != "scrap" {
		return ""
	}
	for _, e := range execs {
		if e.Succeeded && isDaily(e.Args) && e.Created.In(book.Seoul).Format("2006-01-02") == today {
			return "오늘 일일 배치가 이미 성공했습니다(실행 " + e.Name + ", " + e.Created.In(book.Seoul).Format("15:04") + " 시작). 밴드 수집은 하루 한 번만 합니다."
		}
	}
	return ""
}

// isDaily는 실행 인자가 일일 배치(batch run)인지 알려 준다. 인자를 덮어쓰지 않은 실행은 이미지의 기본 CMD(run)다.
func isDaily(args []string) bool {
	return len(args) == 0 || args[0] == "run"
}
