//本程式作用為找股票最近4季的EPS，Q1~Q4由近到遠(Q1為最近1季)
package main

import (
	"fmt"
	"log"
	"strconv"
	"time"
	"context"
	"github.com/chromedp/chromedp"
)


// TWSEStockPrice 台灣證交所每日股價 API 回應格式
type TWSEStockPrice struct {
	Data [][]string `json:"data"`
}

// TWSEFinancialData 台灣證交所 API 回應格式
type TWSEFinancialData struct {
	Code      string `json:"Code"`
	PER       string `json:"PER"`
	PBR       string `json:"PBR"`
	ROE       string `json:"ROE"`
	BookValue string `json:"BookValue"`
	EPS       string `json:"EPS"`
}

// fetchEPSFromCMoney 爬取 CMoney 財報 (`f00041`) 來獲取指定股票最近 4 季 EPS
func fetchEPSFromCMoney(ctx context.Context, stockCode string) ([4]float64, error) {
	var epsList [4]float64

	url := fmt.Sprintf("https://www.cmoney.tw/finance/f00041.aspx?s=%s", stockCode)
	var epsTexts [4]string

	// 設定 60 秒超時，確保爬取不會過早取消
	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// **等待 EPS 欄位出現**
	err := chromedp.Run(timeoutCtx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`//td[contains(text(),'每股稅後盈餘(元)')]`, chromedp.BySearch),
		chromedp.Sleep(2*time.Second), // **確保 JavaScript 已渲染**
	)
	if err != nil {
		log.Println("❌ CMoney 爬取 EPS 失敗:", err)
		return epsList, err
	}

	// **抓取最近 4 季 EPS**
	err = chromedp.Run(timeoutCtx,
		chromedp.Text(`//td[contains(text(),'每股稅後盈餘(元)')]/following-sibling::td[1]`, &epsTexts[0], chromedp.NodeVisible),
		chromedp.Text(`//td[contains(text(),'每股稅後盈餘(元)')]/following-sibling::td[2]`, &epsTexts[1], chromedp.NodeVisible),
		chromedp.Text(`//td[contains(text(),'每股稅後盈餘(元)')]/following-sibling::td[3]`, &epsTexts[2], chromedp.NodeVisible),
		chromedp.Text(`//td[contains(text(),'每股稅後盈餘(元)')]/following-sibling::td[4]`, &epsTexts[3], chromedp.NodeVisible),
	)
	if err != nil {
		log.Println("❌ EPS 爬取失敗:", err)
		return epsList, err
	}

	// **解析最近 4 季 EPS**
	for i := 0; i < 4; i++ {
		eps, err := strconv.ParseFloat(epsTexts[i], 64)
		if err != nil {
			log.Printf("⚠️ EPS 轉換失敗 (第 %d 季): %s\n", i+1, epsTexts[i])
			eps = 0.0
		}
		epsList[i] = eps
	}

	return epsList, nil
}


func main() {
	// **允許使用者輸入股票代號**
	var stockCode string
	fmt.Print("請輸入股票代號 (例如 2330): ")
	fmt.Scanln(&stockCode)

	// **建立 Chromedp 瀏覽器**
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true), // ✅ 使用 headless 模式，加速執行
		chromedp.Flag("disable-gpu", true),
	)
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// **開始爬取 EPS**
	epsList, err := fetchEPSFromCMoney(ctx, stockCode)
	if err != nil {
		fmt.Println("❌ 無法取得 EPS:", err)
		return
	}

	// **顯示結果**
	fmt.Printf("=== 股票 %s 最近 4 季 EPS ===\n", stockCode)
	fmt.Printf("Q1: %.2f | Q2: %.2f | Q3: %.2f | Q4: %.2f\n", epsList[0], epsList[1], epsList[2], epsList[3])
	fmt.Printf("總 EPS: %.2f\n", epsList[0]+epsList[1]+epsList[2]+epsList[3])
}
