// batch는 일일 배치와 그 준비 작업을 실행한다.
//
//	batch migrate   DB 스키마를 최신으로 올린다.
//	batch unlock    마이그레이션 중에 강제로 끝나서 남은 마이그레이션 잠금을 푼다.
//	batch login     브라우저 창을 띄워 밴드에 로그인하고 세션을 저장한다.
//	batch run       새 글, 지난번에 처리하지 못한 글, 새 댓글이 달린 글, 최근 글을 처리한다.
//	batch status    최근 실행 기록과 다시 시도할 글을 보여 준다.
//	batch weekly    카카오톡 대화방의 지난주 대화를 요약해서 밴드에 올린다(월요일 아침 실행).
//	batch seed      로컬 DB에 가상의 데모 데이터를 넣는다(make db-seed).
//	batch probe     세션 복사본으로 밴드 홈을 만료 전후에 열어 토큰을 다시 발급하는지 시험한다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/config"
	"github.com/yonsei-alumni/abc-library/internal/extract"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
	"github.com/yonsei-alumni/abc-library/internal/pipeline"
	"github.com/yonsei-alumni/abc-library/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	var err error
	switch os.Args[1] {
	case "migrate":
		err = migrate(ctx, log)
	case "unlock":
		err = unlock(ctx, log)
	case "login":
		err = login()
	case "run":
		err = run(ctx, log, os.Args[2:])
		alertSession(ctx, log, "run", err)
	case "status":
		err = status(ctx, log)
	case "weekly":
		err = summarizeWeek(ctx, log, os.Args[2:])
		alertSession(ctx, log, "weekly", err)
	case "seed":
		err = seed(ctx, log, os.Args[2:])
	case "probe":
		err = probe(log)
	default:
		usage()
	}
	if err != nil {
		log.Error("배치 실패", "명령", os.Args[1], "오류", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "사용법: batch <migrate|unlock|login|run|status|weekly|seed|probe> [-limit N] [-recheck-days N] [-redo-from N] [-redo-posts N,N]")
	os.Exit(2)
}

func statePath() string {
	return config.Get("BAND_STATE_PATH", "band-state.json")
}

func openStore(ctx context.Context, log *slog.Logger) (*store.Store, error) {
	st, err := connect()
	if err != nil {
		return nil, err
	}
	applied, err := st.Migrate(ctx)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	if applied != "" {
		log.Info("마이그레이션 적용", "묶음", applied)
	}
	return st, nil
}

// connect는 마이그레이션을 적용하지 않고 DB에 연결한다.
func connect() (*store.Store, error) {
	dsn, err := config.DatabaseURL()
	if err != nil {
		return nil, err
	}
	return store.Open(dsn)
}

// unlock은 남은 마이그레이션 잠금을 푼다. 잠금이 남으면 migrate뿐 아니라 마이그레이션을 먼저 적용하는 모든 하위 명령이 막힌다.
func unlock(ctx context.Context, log *slog.Logger) error {
	st, err := connect()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.UnlockMigrations(ctx); err != nil {
		return err
	}
	log.Info("마이그레이션 잠금을 풀었다")
	return nil
}

func migrate(ctx context.Context, log *slog.Logger) error {
	st, err := openStore(ctx, log)
	if err != nil {
		return err
	}
	return st.Close()
}

func login() error {
	if err := band.InstallBrowser(); err != nil {
		return err
	}
	if err := band.Login(statePath(), 10*time.Minute); err != nil {
		return err
	}
	fmt.Printf("세션을 %s에 저장했습니다. 이 파일은 비밀번호처럼 다루세요.\n", statePath())
	// 로그인 유지가 먹혔는지 보려고 만료 시각만 보여 준다. 쿠키 값은 출력하지 않는다.
	if lifetimes, err := band.SessionLifetimes(statePath()); err == nil {
		for _, l := range lifetimes {
			fmt.Println(l.Describe(time.Now()))
		}
	}
	return nil
}

func run(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	limit := fs.Int("limit", 30, "한 번에 Claude로 추출할 게시글 수의 상한(0이면 제한 없음)")
	recheckDays := fs.Int("recheck-days", 7, "수정된 본문과 댓글을 다시 확인할 최근 게시글의 기간(일). 새 댓글은 기간과 상관없이 찾는다")
	redoFrom := fs.Int("redo-from", 0, "이 번호부터는 이미 저장한 글도 다시 추출한다(프롬프트를 바꾼 뒤에 쓴다)")
	redoPosts := fs.String("redo-posts", "", "이미 저장했어도 다시 추출할 글 번호를 쉼표로 나열한다(예: 175,184)")
	_ = fs.Parse(args)
	var redoList []int
	for _, s := range strings.Split(*redoPosts, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		no, err := strconv.Atoi(s)
		if err != nil || no < 1 {
			return fmt.Errorf("-redo-posts의 글 번호가 잘못되었습니다: %q", s)
		}
		redoList = append(redoList, no)
	}

	st, err := openStore(ctx, log)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	client, err := extract.NewClient()
	if err != nil {
		return err
	}
	if err := band.InstallHeadlessBrowser(); err != nil {
		return err
	}
	crawler, err := band.Open(statePath(), config.BandNo())
	if err != nil {
		return err
	}
	defer func() { _ = crawler.Close() }()

	// 태그는 관리 화면에서 고친 DB의 목록에서만 고른다.
	tags, err := st.Tags(ctx)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return errors.New("태그 목록이 비어 있습니다. 관리 화면에서 태그를 더하세요")
	}
	ex := extract.New(&client.Beta.Messages, kyobo.NewClient(), tags)
	_, err = pipeline.Run(ctx, crawler, ex, st, pipeline.Options{Limit: *limit, RecheckDays: *recheckDays, RedoFrom: *redoFrom, RedoPosts: redoList}, log)
	return err
}

func status(ctx context.Context, log *slog.Logger) error {
	st, err := openStore(ctx, log)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	last, err := st.LastPostNo(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("저장된 마지막 글: %d\n\n최근 실행\n", last)
	runs, err := st.RecentRuns(ctx, 5)
	if err != nil {
		return err
	}
	for _, r := range runs {
		finished := "실행 중이거나 비정상 종료"
		if !r.FinishedAt.IsZero() {
			finished = r.FinishedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("  #%d %s ~ %s | 글 %d→%d | 확인 %d, 추출 %d, 변경 없음 %d, 없는 글 %d, 실패 %d, 책 %d",
			r.ID, r.StartedAt.Local().Format("2006-01-02 15:04"), finished, r.LastPostNo, r.LatestPostNo,
			r.Checked, r.Extracted, r.Unchanged, r.Missing, r.Failed, r.Books)
		if r.Error != "" {
			fmt.Printf(" | 오류: %s", r.Error)
		}
		fmt.Println()
	}
	pending, err := st.PendingPosts(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("\n다시 시도할 글 (최대 %d번 시도)\n", pipeline.MaxAttempts)
	if len(pending) == 0 {
		fmt.Println("  없음")
	}
	for _, p := range pending {
		fmt.Printf("  글 %d | %s | %d번 시도 | %s", p.BandPostNo, p.Reason, p.Attempts, p.LastTriedAt.Local().Format("2006-01-02 15:04"))
		if p.LastError != "" {
			fmt.Printf(" | %s", p.LastError)
		}
		fmt.Println()
	}
	return nil
}
