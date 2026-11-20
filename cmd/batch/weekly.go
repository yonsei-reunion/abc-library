package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/claude"
	"github.com/yonsei-alumni/abc-library/internal/config"
	"github.com/yonsei-alumni/abc-library/internal/kakao"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
	"github.com/yonsei-alumni/abc-library/internal/weekly"
)

// summarizeWeek는 카카오톡 대화방의 한 주(월~일) 대화를 Google 스프레드시트에서 읽어 요약하고, 밴드 웹 화면에서 글로 올린다.
// 수집과 같은 밴드 로그인 세션(BAND_STATE_PATH)을 쓴다.
// 메시지 원문과 작성자는 DB에 저장하지 않고 로그에도 남기지 않는다.
func summarizeWeek(ctx context.Context, log *slog.Logger, args []string) (string, error) {
	fs := flag.NewFlagSet("weekly", flag.ExitOnError)
	end := fs.String("end", "", "요약할 7일의 마지막 날(YYYY-MM-DD). 비우면 지난 일요일")
	dryRun := fs.Bool("dry-run", false, "밴드에 올리지 않고 화면에 출력한다")
	bandNo := fs.String("band-no", config.BandNo(), "글을 올릴 밴드의 번호. 시험용 밴드에 올릴 때 바꾼다")
	_ = fs.Parse(args)

	start, last, err := weekly.Period(time.Now(), *end)
	if err != nil {
		return "", err
	}
	room := config.Get("KAKAO_ROOM", "ABC(아카라카북클럽)")
	if *dryRun {
		sum, err := weeklySummary(ctx, log, room, start, last)
		if err != nil {
			return "", err
		}
		fmt.Println(sum.Text())
		return "요약을 화면에 출력했습니다.", nil
	}

	if err := band.InstallHeadlessBrowser(); err != nil {
		return "", err
	}
	crawler, err := band.Open(statePath(), *bandNo)
	if err != nil {
		return "", err
	}
	defer func() { _ = crawler.Close() }()
	crawler.Board = config.Get("BAND_BOARD", "자유게시판")
	// 이미 올린 주면 Claude를 부르기 전에 끝낸다.
	posted, err := crawler.Posted(weekly.Summary{Start: start, End: last}.PeriodLine())
	if err != nil {
		return "", err
	}
	if posted {
		log.Info("같은 기간의 요약이 이미 밴드에 있어서 올리지 않는다", "시작", start, "끝", last)
		return alreadyPosted(start, last), nil
	}
	sum, err := weeklySummary(ctx, log, room, start, last)
	if err != nil {
		return "", err
	}
	return postWeekly(log, crawler, sum)
}

// poster는 밴드에 글을 올린다. *band.Crawler가 구현한다.
type poster interface {
	Posted(marker string) (bool, error)
	Write(content, marker string) error
}

// postWeekly는 요약을 밴드에 올리고 결과를 한 줄로 돌려준다. 메시지가 없는 주와, 같은 기간의 요약을 이미 올린 주는 올리지 않는다.
// 배치를 다시 실행해도 두 번 올리지 않도록 밴드의 최근 글에서 기간 줄을 찾는다.
func postWeekly(log *slog.Logger, b poster, sum weekly.Summary) (string, error) {
	if sum.Count == 0 {
		log.Warn("이 기간에 수집된 메시지가 없어서 밴드에 올리지 않는다", "시작", sum.Start, "끝", sum.End)
		return fmt.Sprintf("%s~%s에 수집된 메시지가 없어서 밴드에 올리지 않았습니다.", sum.Start, sum.End), nil
	}
	marker := sum.PeriodLine()
	posted, err := b.Posted(marker)
	if err != nil {
		return "", err
	}
	if posted {
		log.Info("같은 기간의 요약이 이미 밴드에 있어서 올리지 않는다", "시작", sum.Start, "끝", sum.End)
		return alreadyPosted(sum.Start, sum.End), nil
	}
	if err := b.Write(sum.Text(), marker); err != nil {
		return "", err
	}
	log.Info("대화방 주간 요약을 밴드에 올렸다", "시작", sum.Start, "끝", sum.End, "책", len(sum.Books), "링크", len(sum.Links))
	return fmt.Sprintf("%s~%s 대화방 주간 요약을 밴드에 올렸습니다(책 %d, 링크 %d).", sum.Start, sum.End, len(sum.Books), len(sum.Links)), nil
}

func alreadyPosted(start, end string) string {
	return fmt.Sprintf("%s~%s 요약이 이미 밴드에 있어서 올리지 않았습니다.", start, end)
}

func weeklySummary(ctx context.Context, log *slog.Logger, room, start, end string) (weekly.Summary, error) {
	sheet := kakao.NewClient(os.Getenv("KAKAO_SHEET_URL"), os.Getenv("KAKAO_SHEET_TOKEN"))
	// 날짜를 모르는 메시지를 앞뒤 메시지로 채우고 대화 순서로 정렬해 둔다. 실패해도 시트를 그대로 읽어 요약한다.
	if res, err := sheet.Arrange(ctx); err != nil {
		log.Warn("시트를 정리하지 못해서 그대로 읽는다", "오류", err)
	} else {
		log.Info("시트 정리", "행", res.Rows, "날짜 추정", res.Inferred, "날짜 모름", res.StillBlank)
	}
	all, err := sheet.Messages(ctx, room, start, end)
	if err != nil {
		return weekly.Summary{}, err
	}
	msgs := kakao.Week(all, start, end)
	log.Info("대화방 메시지", "시작", start, "끝", end, "받은 행", len(all), "기간 안", len(msgs))
	in := weekly.Input{Room: room, Start: start, End: end, Messages: msgs}
	if len(msgs) == 0 {
		return weekly.Summary{Room: room, Start: start, End: end}, nil
	}

	client, err := claude.NewClient()
	if err != nil {
		return weekly.Summary{}, err
	}
	// 책 상세 화면 링크는 덤이다. DB에 연결하지 못하면 링크 없이 요약한다.
	var books weekly.BookFinder
	if st, err := connect(); err == nil {
		defer func() { _ = st.Close() }()
		books = st
	} else {
		log.Warn("DB에 연결하지 못해서 사이트 링크 없이 요약한다", "오류", err)
	}
	s := weekly.New(&client.Beta.Messages, kyobo.NewClient(), weekly.NewPageTitles(), books, config.Get("SITE_URL", "https://abc-lib.web.app"))
	return s.Summarize(ctx, in)
}
