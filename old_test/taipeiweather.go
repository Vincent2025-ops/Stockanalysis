package main

import (
	"fmt"
	"log"
	"os"

	"github.com/playwright-community/playwright-go"
)

func main() {
	// 啟動 Playwright
	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("❌ 無法啟動 Playwright: %v", err)
	}
	defer pw.Stop()

	// 啟動瀏覽器（無頭模式）
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		log.Fatalf("❌ 無法啟動瀏覽器: %v", err)
	}
	defer browser.Close()

	// 開啟新頁面
	page, err := browser.NewPage()
	if err != nil {
		log.Fatalf("❌ 無法開啟頁面: %v", err)
	}

	// 訪問氣象網站
	url := "https://www.cwa.gov.tw/V8/C/W/Town/Town.html?TID=6301000"
	fmt.Println("🔍 正在訪問:", url)
	_, err = page.Goto(url, playwright.PageGotoOptions{
		Timeout: playwright.Float(30000),
	})
	if err != nil {
		log.Fatalf("❌ 無法訪問網頁: %v", err)
	}

	// 等待溫度資訊加載
	page.WaitForSelector(".temperature span")

	// 等待天氣描述加載（.marquee）
	page.WaitForSelector(".marquee", playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(60000), // ✅ 增加等待時間
	})

	// 抓取溫度
	temp, err := page.TextContent(".temperature span")
	if err != nil {
		log.Println("⚠️ 無法抓取溫度:", err)
		temp = "N/A"
	}

	// 抓取天氣描述（使用 .marquee）
	weather, err := page.TextContent(".marquee") // ✅ 修改選擇器
	if err != nil {
		log.Println("⚠️ 無法抓取天氣描述:", err)
		weather = "N/A"
	}

	// 顯示結果
	fmt.Printf("🌡 台北市氣溫: %s°C\n", temp)
	fmt.Printf("🌦 天氣狀況: %s\n", weather)

	// 儲存到檔案
	saveWeatherData(temp, weather)
}

// 儲存天氣資訊到檔案
func saveWeatherData(temp, weather string) {
	file, err := os.OpenFile("taipei_weather.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("❌ 無法打開檔案: %v", err)
	}
	defer file.Close()

	data := fmt.Sprintf("溫度: %s°C, 天氣: %s\n", temp, weather)
	if _, err := file.WriteString(data); err != nil {
		log.Fatalf("❌ 無法寫入檔案: %v", err)
	}

	fmt.Println("✅ 資料已儲存至 taipei_weather.txt")
}