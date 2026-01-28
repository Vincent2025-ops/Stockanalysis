//本支程式可輸入股票代號，依當日收盤價及近4季eps算出本益比P/E、股價淨值比P/B、股東權益報酬率ROE，並確認是否為成長股或低估股
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"time"
	"github.com/chromedp/chromedp"
)

// 包含股票代號、P/B 比率、ROE 及每股淨值
// 目前 TWSE API 可能不提供 BookValue，需要用 PBratio 計算

// TWSEFinancialData 用來解析台灣證交所 API 回應
type TWSEFinancialData struct {
	Code       string `json:"Code"`        // 股票代號
	PBR        string `json:"PBR"`         // 股價淨值比
	ROE        string `json:"ROE"`         // 股東權益報酬率
	BookValue  string `json:"BookValue"`   // 每股淨值(可能缺失)
}


// StockData 存放個股財務資訊
type StockData struct {
	Ticker    string  // 股票代號
	Name      string  // 股票名稱
	Price     float64 // 收盤價
	EPS_Q1    float64 // 第一季 EPS
	EPS_Q2    float64 // 第二季 EPS
	EPS_Q3    float64 // 第三季 EPS
	EPS_Q4    float64 // 第四季 EPS
	EPS       float64 // 最近四季 EPS 總和
	BookValue float64 // 每股淨值 (若 TWSE API 無提供，則計算取得)
	PE        float64 // 本益比 (Price / EPS)
	PB        float64 // 股價淨值比 (Price / BookValue)
	ROE       float64 // 股東權益報酬率 (EPS / BookValue * 100%)
	IsUndervalued bool  // 是否被低估
	IsGrowthStock bool  // 是否為潛力成長股
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


// fetchStockPrice 從 Yahoo Finance 取得最新收盤價
func fetchStockPrice(ctx context.Context, stockCode string) (float64, error) {
	url := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s.TW", stockCode)
	client := &http.Client{Timeout: 10 * time.Second}

	log.Println("🔍 正在訪問 Yahoo Finance:", url)

	// 建立 HTTP 請求，加入 User-Agent
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Println("❌ 建立 Yahoo Finance API 請求失敗:", err)
		return 0, fmt.Errorf("無法獲取收盤價")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	// 發送請求
	resp, err := client.Do(req)
	if err != nil {
		log.Println("❌ Yahoo Finance API 請求失敗:", err)
		return 0, fmt.Errorf("無法獲取收盤價")
	}
	defer resp.Body.Close()

	// 確保 API 回應 200 OK
	if resp.StatusCode != 200 {
		log.Println("❌ Yahoo Finance API 回應錯誤:", resp.Status)
		return 0, fmt.Errorf("API 回應錯誤: %s", resp.Status)
	}

	// 讀取 API 回應內容
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("❌ 讀取 Yahoo Finance API 回應失敗:", err)
		return 0, fmt.Errorf("讀取 API 回應失敗")
	}

	// 輸出 API 回應內容，查看是否為 JSON
	//log.Println("📄 Yahoo Finance API 回應內容:", string(body))

	// 解析 JSON 結構
	var yahooResponse struct {
		Chart struct {
			Result []struct {
				Meta struct {
					RegularMarketPrice float64 `json:"regularMarketPrice"`
				} `json:"meta"`
			} `json:"result"`
		} `json:"chart"`
	}

	if err := json.Unmarshal(body, &yahooResponse); err != nil {
		log.Println("❌ Yahoo Finance JSON 解析失敗:", err)
		return 0, fmt.Errorf("無法解析 API 資料")
	}

	// 確保數據有效
	if len(yahooResponse.Chart.Result) == 0 {
		log.Println("❌ Yahoo Finance API 無回應數據")
		return 0, fmt.Errorf("無法取得收盤價")
	}

	closingPrice := yahooResponse.Chart.Result[0].Meta.RegularMarketPrice
	log.Println("✅ 取得 Yahoo Finance 收盤價:", closingPrice)

	return closingPrice, nil
}


// fetchFinancialData 爬取 EPS 並計算 P/E、P/B、ROE，並取得股票名稱
func fetchFinancialData(ctx context.Context, stockCode string) (StockData, error) {
	var stock StockData
	stock.Ticker = stockCode

	// **從 TWSE API 取得股票名稱**
	url := "https://openapi.twse.com.tw/v1/exchangeReport/BWIBBU_ALL"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return stock, fmt.Errorf("TWSE API 請求失敗: %v", err)
	}
	defer resp.Body.Close()

	// 讀取 API 回應內容
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return stock, fmt.Errorf("讀取 API 回應失敗: %v", err)
	}

	// 解析 JSON
	var financialData []struct {
		Code string `json:"Code"`
		Name string `json:"Name"`
	}

	if err := json.Unmarshal(body, &financialData); err != nil {
		return stock, fmt.Errorf("解析 TWSE API 失敗: %v", err)
	}

	// **尋找股票代號對應的名稱**
	for _, data := range financialData {
		if data.Code == stockCode {
			stock.Name = data.Name
			break
		}
	}

	// **如果找不到名稱，設為 "未知"**
	if stock.Name == "" {
		stock.Name = "未知"
	}
	
	// **從 CMoney 爬取 EPS**
	epsList, err := fetchEPSFromCMoney(ctx, stockCode)
	if err != nil {
		log.Println("❌ EPS 爬取失敗:", err)
		epsList = [4]float64{0.0, 0.0, 0.0, 0.0}
	}
	stock.EPS_Q1, stock.EPS_Q2, stock.EPS_Q3, stock.EPS_Q4 = epsList[0], epsList[1], epsList[2], epsList[3]
	stock.EPS = stock.EPS_Q1 + stock.EPS_Q2 + stock.EPS_Q3 + stock.EPS_Q4

	// **從 Yahoo Finance 爬取最新收盤價**
	price, err := fetchStockPrice(ctx, stockCode)
	if err != nil {
		return stock, fmt.Errorf("❌ 無法獲取收盤價: %v", err)
	}
	stock.Price = price

	// **從 TWSE API 計算 BookValue**
	bookValue, err := fetchBookValue(stockCode, price)
	if err != nil {
		log.Println("⚠️ 無法取得每股淨值，使用預設值 100.0")
		stock.BookValue = 100.0
	} else {
		stock.BookValue = bookValue
	}

	// **計算 P/E、P/B、ROE**
	if stock.EPS > 0 {
		stock.PE = stock.Price / stock.EPS
	} else {
		stock.PE = -1 // **EPS 為 0 或負數時，無法計算 P/E**
	}
	if stock.BookValue > 0 {
		stock.PB = stock.Price / stock.BookValue
		stock.ROE = (stock.EPS / stock.BookValue) * 100
	} else {
		stock.PB = -1  // **BookValue 無法取得時，P/B 設為 -1**
		stock.ROE = -1 // **BookValue 無法取得時，ROE 設為 -1**
	}


	return stock, nil
}


// fetchBookValue 爬取台灣證交所 API，獲取每股淨值 (BookValue)
func fetchBookValue(stockCode string, stockPrice float64) (float64, error) {
	url := "https://openapi.twse.com.tw/v1/exchangeReport/BWIBBU_ALL"
	client := &http.Client{Timeout: 10 * time.Second}

	log.Println("🔍 正在訪問 TWSE API:", url)

	resp, err := client.Get(url)
	if err != nil {
		log.Println("❌ TWSE API 請求失敗:", err)
		return 0, fmt.Errorf("TWSE API 請求失敗")
	}
	defer resp.Body.Close()

	// 確保 API 回應 200 OK
	if resp.StatusCode != 200 {
		log.Println("❌ TWSE API 回應錯誤:", resp.Status)
		return 0, fmt.Errorf("API 回應錯誤: %s", resp.Status)
	}

	// 讀取 API 回應內容
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("❌ 讀取 TWSE API 回應失敗:", err)
		return 0, fmt.Errorf("讀取 API 回應失敗")
	}

	// 解析 JSON
	var financialData []struct {
		Code    string `json:"Code"`
		PBratio string `json:"PBratio"`
	}

	if err := json.Unmarshal(body, &financialData); err != nil {
		log.Println("❌ JSON 解析失敗:", err)
		return 0, fmt.Errorf("解析 TWSE API 失敗")
	}

	// **尋找股票代號對應的 PBratio**
	for _, data := range financialData {
		if data.Code == stockCode {
			pbRatio, err := strconv.ParseFloat(data.PBratio, 64)
			if err != nil {
				log.Println("❌ 轉換 PBratio 失敗:", data.PBratio)
				return 0, fmt.Errorf("轉換 PBratio 失敗")
			}

			// **計算 BookValue**
			if pbRatio > 0 {
				bookValue := stockPrice / pbRatio
				log.Println("✅ 計算得到的每股淨值 (BookValue):", bookValue)
				return bookValue, nil
			}
		}
	}

	log.Println("⚠️ TWSE API 無法找到", stockCode, "的 PBratio")
	return 0, fmt.Errorf("無法取得 %s 的 PBratio", stockCode)
}


// analyzeStock 分析股票是否低估或成長
func analyzeStock(stock *StockData) {
	// 低估標準: P/E < 15 且 P/B < 1.5 且 ROE > 10%
	stock.IsUndervalued = stock.PE > 0 && stock.PE < 15 && stock.PB < 1.5 && stock.ROE > 10

	// 成長股標準: EPS > 2（避免負數）且 ROE > 15% 且 P/E < 30
	stock.IsGrowthStock = stock.EPS > 2 && stock.ROE > 15 && stock.PE > 0 && stock.PE < 30
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

	// **開始爬取財務數據**
	stockData, err := fetchFinancialData(ctx, stockCode)
	if err != nil {
		fmt.Println("❌ 無法取得財務數據:", err)
		return
	}

	// **分析是否為低估股或成長股**
	analyzeStock(&stockData)

	// **美化輸出**
	fmt.Println("\n===================================")
	//fmt.Printf(" 📈 股票代號: %s\n", stockData.Ticker)
	fmt.Printf(" 📈 股票代號: %s(%s)\n", stockData.Ticker, stockData.Name)
	fmt.Println("===================================")
	fmt.Printf(" 📌 收盤價:        %8.2f 元\n", stockData.Price)
	fmt.Printf(" 📊 EPS (近4季):   Q1: %5.2f | Q2: %5.2f | Q3: %5.2f | Q4: %5.2f\n",
		stockData.EPS_Q1, stockData.EPS_Q2, stockData.EPS_Q3, stockData.EPS_Q4)
	fmt.Printf(" 🔢 年度 EPS:      %8.2f 元\n", stockData.EPS)

	if stockData.PE > 0 {
		fmt.Printf(" 📉 本益比 (P/E):  %8.2f 倍\n", stockData.PE)
	} else {
		fmt.Println(" 📉 本益比 (P/E):     無法計算")
	}

	fmt.Printf(" 🏦 股價淨值比 (P/B): %6.2f 倍\n", stockData.PB)
	fmt.Printf(" 💰 ROE (股東權益報酬率): %6.2f %%\n", stockData.ROE)

	// **輸出是否為低估股或成長股**
	fmt.Println("-----------------------------------")
	if stockData.IsUndervalued {
		fmt.Println(" 💎 這是一支 **低估股**（P/E < 15, P/B < 1.5, ROE > 10%）")
	} else {
		fmt.Println(" ⚠️ 這支股票 **沒有被低估**")
	}
	if stockData.IsGrowthStock {
		fmt.Println(" 🚀 這是一支 **成長股**（EPS > 2, ROE > 15%, P/E < 30）")
	} else {
		fmt.Println(" 🔍 這支股票 **不是成長股**")
	}
	fmt.Println("===================================")
}
