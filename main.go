package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cihub/seelog"
	"github.com/yincongcyincong/go12306/action"
	http12306 "github.com/yincongcyincong/go12306/http"
	"github.com/yincongcyincong/go12306/module"
	"github.com/yincongcyincong/go12306/utils"
)

var (
	runType    = flag.String("run_type", "command", "web：网页模式")
	wxrobot    = flag.String("wxrobot", "", "企业微信机器人通知")
	mustDevice = flag.String("must_device", "0", "强制生成设备信息")
)

func main() {
	flag.Parse()
	Init()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	select {
	case <-sigs:
		seelog.Info("用户登出")
		utils.SaveConf()
		action.LoginOut()
	}
}

func Init() {
	initLog()
	initUtil()
	initHttp()

	if *runType == "command" {
		go CommandStart()
	}
}

func initUtil() {
	// 用户自己设置设置device信息
	utils.InitConf(*wxrobot)
	railExpStr := utils.GetCookieVal("RAIL_EXPIRATION")
	railExp, _ := strconv.Atoi(railExpStr)
	if railExp <= int(time.Now().Unix()*1000) || *mustDevice == "1" {
		seelog.Info("开始重新获取设备信息")
		utils.GetDeviceInfo()
	}

	if utils.GetCookieVal("RAIL_DEVICEID") == "" || utils.GetCookieVal("RAIL_EXPIRATION") == "" {
		panic("生成device信息失败, 请手动把cookie信息复制到./conf/conf.ini文件中")
	}

	utils.SaveConf()
	utils.InitBlacklist()
	utils.InitAvailableCDN()
}

func initLog() {
	logType := `<console/>`
	if *runType == "web" {
		logType = `<file path="log/log.log"/>`
	}

	logger, err := seelog.LoggerFromConfigAsString(`<seelog type="sync" minlevel="info">
    <outputs formatid="main">
        ` + logType + `
    </outputs>
	<formats>
        <format id="main" format="%Date %Time [%LEV] %RelFile:%Line - %Msg%n"></format>
    </formats>
</seelog>`)
	if err != nil {
		log.Panicln(err)
	}
	err = seelog.ReplaceLogger(logger)
	if err != nil {
		log.Panicln(err)
	}
}

func initHttp() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				seelog.Error(err)
				seelog.Flush()
			}
		}()

		http.HandleFunc("/create-image", http12306.CreateImageReq)
		http.HandleFunc("/login", http12306.QrLoginReq)
		http.HandleFunc("/hb", http12306.StartHBReq)
		http.HandleFunc("/logout", http12306.UserLogoutReq)
		http.HandleFunc("/search-train", http12306.SearchTrain)
		http.HandleFunc("/search-info", http12306.SearchInfo)
		http.HandleFunc("/check-login", http12306.IsLogin)
		http.HandleFunc("/order", http12306.StartOrderReq)
		http.HandleFunc("/re-login", http12306.ReLogin)
		http.HandleFunc("/", http12306.LoginView)
		http.HandleFunc("/send-msg", http12306.SendMsg)
		if err := http.ListenAndServe(":28178", nil); err != nil {
			log.Panicln(err)
		}
	}()
}

func CommandStart() {
	defer func() {
		if err := recover(); err != nil {
			seelog.Error(err)
			seelog.Flush()
		}
	}()

	var err error
	if err = action.GetLoginData(); err != nil {
		qrImage, err := action.CreateImage()
		if err != nil {
			seelog.Errorf("创建二维码失败:%v", err)
			return
		}
		qrImage.Image = ""

		err = action.QrLogin(qrImage)
		if err != nil {
			seelog.Errorf("登陆失败:%v", err)
			return
		}
	}

	startCheckLogin()

	// Reorder:
	searchParam := new(module.SearchParam)
	var trainStr, seatStr, passengerStr string
	isNate := "0"
	for i := 1; i < math.MaxInt64; i++ {
		getUserInfo(searchParam, &trainStr, &seatStr, &passengerStr, &isNate)
		if trainStr != "" && seatStr != "" && passengerStr != "" {
			break
		}

		time.Sleep(1 * time.Second)
	}

	// 开始轮训买票
	trainMap := utils.GetBoolMap(strings.Split(trainStr, "#"))
	passengerMap := utils.GetBoolMap(strings.Split(passengerStr, "#"))
	seatSlice := strings.Split(seatStr, "#")

Search:
	var trainData *module.TrainData
	var isAfterNate bool
	for i := 0; i < math.MaxInt64; i++ {
		trainData, isAfterNate, err = getTrainInfo(searchParam, trainMap, seatSlice, isNate)
		if err == nil {
			break
		} else {
			time.Sleep(time.Duration(utils.GetRand(utils.SearchInterval[0], utils.SearchInterval[1])) * time.Millisecond)
		}
	}

	if isAfterNate {
		seelog.Info("开始候补", trainData.TrainNo)
		err = startAfterNate(searchParam, trainData, passengerMap)
	} else {
		seelog.Info("开始购买", trainData.TrainNo)
		err = startOrder(searchParam, trainData, passengerMap)
	}

	if err != nil {
		// utils.AddBlackList(trainData.TrainNo)
		time.Sleep(time.Duration(utils.GetRand(utils.SearchInterval[0], utils.SearchInterval[1])) * time.Millisecond)
		goto Search
	}

	if *wxrobot != "" {
		utils.SendWxrootMessage(*wxrobot, fmt.Sprintf("车次：%s 购买成功, 请登陆12306查看", trainData.TrainNo))
	}
	// goto Reorder
}

func getTrainInfo(searchParam *module.SearchParam, trainMap map[string]bool, seatSlice []string, isNate string) (*module.TrainData, bool, error) {
	// 如果在晚上11点到早上5点之间，停止抢票，只自动登陆
	waitToOrder()

	var err error
	searchParam.SeatType = ""
	var trainData *module.TrainData

	trains, err := action.GetTrainInfo(searchParam)
	if err != nil {
		seelog.Errorf("查询车站失败:%v", err)
		return nil, false, err
	}

	for _, t := range trains {
		// 在选中的，但是不在小黑屋里面
		if utils.InBlackList(t.TrainNo) {
			seelog.Info(t.TrainNo, "在小黑屋，需等待60s")
			continue
		}

		if trainMap[t.TrainNo] {
			for _, s := range seatSlice {
				if t.SeatInfo[s] != "" && t.SeatInfo[s] != "无" {
					trainData = t
					searchParam.SeatType = utils.OrderSeatType[s]
					seelog.Infof("%s %s 数量: %s", t.TrainNo, s, t.SeatInfo[s])
					return trainData, false, nil
				}
				seelog.Infof("%s %s 数量: %s", t.TrainNo, s, t.SeatInfo[s])
			}
		}
	}

	// 判断是否可以候补
	if (isNate == "1" || isNate == "是") && searchParam.SeatType == "" {
		for _, t := range trains {
			// 在选中的，但是不在小黑屋里面
			if utils.InBlackList(t.TrainNo) {
				seelog.Info(t.TrainNo, "在小黑屋，需等待60s")
				continue
			}

			if trainMap[t.TrainNo] {
				for _, s := range seatSlice {
					if t.SeatInfo[s] == "无" && t.IsCanNate == "1" {
						trainData = t
						searchParam.SeatType = utils.OrderSeatType[s]
						return trainData, true, nil
					}
				}
			}
		}
	}

	if trainData == nil || searchParam.SeatType == "" {
		seelog.Info("暂无车票可以购买")
		return nil, false, errors.New("暂无车票可以购买")
	}

	return trainData, false, nil
}

func getUserInfo(searchParam *module.SearchParam, trainStr, seatStr, passengerStr, isNate *string) {
	fmt.Printf("日期: %s 起始站: %s 到达站: %s\n", utils.TrainInfo.Date, utils.TrainInfo.StartStation, utils.TrainInfo.EndStation)
	// fmt.Scanf("%s %s %s", &searchParam.TrainDate, &searchParam.FromStationName, &searchParam.ToStationName)
	searchParam.TrainDate = utils.TrainInfo.Date
	searchParam.FromStationName = utils.TrainInfo.StartStation
	searchParam.ToStationName = utils.TrainInfo.EndStation
	if searchParam.TrainDate == "" || searchParam.FromStationName == "" || searchParam.ToStationName == "" {
		return
	}
	searchParam.FromStation = utils.Station[searchParam.FromStationName]
	searchParam.ToStation = utils.Station[searchParam.ToStationName]

	if utils.TrainInfo.IssueTime.After(time.Now()) {
		fmt.Printf("未到放票时间: %s\n", utils.TrainInfo.IssueTime)
		time.Sleep(time.Until(utils.TrainInfo.IssueTime) + 5*time.Millisecond)
	}

	// trains, err := action.GetTrainInfo(searchParam)
	// if err != nil {
	// 	seelog.Errorf("查询车站失败:%v", err)
	// 	return
	// }
	// for _, t := range trains {
	// 	fmt.Println(fmt.Sprintf("车次: %s, 状态: %s, 始发车站: %s, 终点站:%s,  %s: %s, 历时：%s, 二等座: %s, 一等座: %s, 商务座: %s, 软卧: %s, 硬卧: %s，软座: %s，硬座: %s， 无座: %s,",
	// 		t.TrainNo, t.Status, t.FromStationName, t.ToStationName, t.StartTime, t.ArrivalTime, t.DistanceTime, t.SeatInfo["二等座"], t.SeatInfo["一等座"], t.SeatInfo["商务座"], t.SeatInfo["软卧"], t.SeatInfo["硬卧"], t.SeatInfo["软座"], t.SeatInfo["硬座"], t.SeatInfo["无座"]))
	// }

	fmt.Printf("车次: %s\n", utils.TrainInfo.TrainNo)
	*trainStr = utils.TrainInfo.TrainNo
	// fmt.Scanf("%s", trainStr)

	fmt.Printf("座位类型: %s\n", utils.TrainInfo.SeatType)
	*seatStr = utils.TrainInfo.SeatType
	// fmt.Scanf("%s", seatStr)

	// submitToken, err := action.GetRepeatSubmitToken()
	// if err != nil {
	// 	seelog.Errorf("获取提交数据失败:%v", err)
	// 	return
	// }
	// passengers, err := action.GetPassengers(submitToken)
	// if err != nil {
	// 	seelog.Errorf("获取用户失败:%v", err)
	// 	return
	// }
	// for _, p := range passengers.Data.NormalPassengers {
	// 	fmt.Printf("乘客：%s\n", p.Alias)
	// }

	fmt.Printf("乘客姓名: %s\n", utils.TrainInfo.Passenger)
	*passengerStr = utils.TrainInfo.Passenger
	// fmt.Scanf("%s", passengerStr)

	fmt.Printf("是否候补: %s\n", utils.TrainInfo.IsNate)
	*isNate = utils.TrainInfo.IsNate
	// fmt.Scanf("%s", isNate)
}

func startOrder(searchParam *module.SearchParam, trainData *module.TrainData, passengerMap map[string]bool) error {
	err := action.GetLoginData()
	if err != nil {
		seelog.Errorf("自动登陆失败：%v", err)
		return err
	}

	err = action.CheckUser()
	if err != nil {
		seelog.Errorf("检查用户状态失败：%v", err)
		return err
	}

	err = action.SubmitOrder(trainData, searchParam)
	if err != nil {
		seelog.Errorf("提交订单失败：%v", err)
		return err
	}

	submitToken, err := action.GetRepeatSubmitToken()
	if err != nil {
		seelog.Errorf("获取提交数据失败：%v", err)
		return err
	}

	passengers, err := action.GetPassengers(submitToken)
	if err != nil {
		seelog.Errorf("获取乘客失败：%v", err)
		return err
	}
	buyPassengers := make([]*module.Passenger, 0)
	for _, p := range passengers.Data.NormalPassengers {
		if passengerMap[p.Alias] {
			buyPassengers = append(buyPassengers, p)
		}
	}

	err = action.CheckOrder(buyPassengers, submitToken, searchParam)
	if err != nil {
		seelog.Errorf("检查订单失败：%v", err)
		return err
	}

	err = action.GetQueueCount(submitToken, searchParam)
	if err != nil {
		seelog.Errorf("获取排队数失败：%v", err)
		return err
	}

	err = action.ConfirmQueue(buyPassengers, submitToken, searchParam)
	if err != nil {
		seelog.Errorf("提交订单失败：%v", err)
		return err
	}

	var orderWaitRes *module.OrderWaitRes
	for i := 0; i < 20; i++ {
		orderWaitRes, err = action.OrderWait(submitToken)
		if err != nil {
			time.Sleep(7 * time.Second)
			continue
		}
		if orderWaitRes.Data.OrderId != "" {
			break
		}
	}

	if orderWaitRes != nil {
		err = action.OrderResult(submitToken, orderWaitRes.Data.OrderId)
		if err != nil {
			seelog.Errorf("获取订单状态失败：%v", err)
		}
	}

	if orderWaitRes == nil || orderWaitRes.Data.OrderId == "" {
		seelog.Infof("购买成功")
		return nil
	}

	seelog.Infof("购买成功，订单号：%s", orderWaitRes.Data.OrderId)
	return nil
}

func startAfterNate(searchParam *module.SearchParam, trainData *module.TrainData, passengerMap map[string]bool) error {
	err := action.GetLoginData()
	if err != nil {
		seelog.Errorf("自动登陆失败：%v", err)
		return err
	}

	err = action.AfterNateChechFace(trainData, searchParam)
	if err != nil {
		seelog.Errorf("人脸验证失败：%v", err)
		return err
	}

	_, err = action.AfterNateSuccRate(trainData, searchParam)
	if err != nil {
		seelog.Errorf("获取候补成功率失败：%v", err)
		return err
	}

	err = action.CheckUser()
	if err != nil {
		seelog.Errorf("检查用户状态失败：%v", err)
		return err
	}

	err = action.AfterNateSubmitOrder(trainData, searchParam)
	if err != nil {
		seelog.Errorf("提交候补订单失败：%v", err)
		return err
	}

	submitToken := &module.SubmitToken{
		Token: "",
	}
	passengers, err := action.GetPassengers(submitToken)
	if err != nil {
		seelog.Errorf("获取乘客失败：%v", err)
		return err
	}
	buyPassengers := make([]*module.Passenger, 0)
	for _, p := range passengers.Data.NormalPassengers {
		if passengerMap[p.Alias] {
			buyPassengers = append(buyPassengers, p)
		}
	}

	err = action.PassengerInit()
	if err != nil {
		seelog.Errorf("初始化乘客信息失败：%v", err)
		return err
	}

	err = action.AfterNateGetQueueNum()
	if err != nil {
		seelog.Errorf("获取候补排队信息失败：%v", err)
		return err
	}

	confirmRes, err := action.AfterNateConfirmHB(buyPassengers, searchParam, trainData)
	if err != nil {
		seelog.Errorf("提交订单失败：%v", err)
		return err
	}

	seelog.Infof("候补成功，订单号：%s", confirmRes.Data.ReserveNo)
	return nil
}

func waitToOrder() {
	if time.Now().Hour() >= 23 || time.Now().Hour() <= 4 {

		for {
			if 5 <= time.Now().Hour() && time.Now().Hour() < 23 {
				break
			}

			time.Sleep(1 * time.Minute)
		}
	}
}

func startCheckLogin() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				seelog.Error(err)
				seelog.Flush()
			}
		}()

		timer := time.NewTicker(2 * time.Minute)
		alTimer := time.NewTicker(10 * time.Minute)
		for {
			select {
			case <-timer.C:
				if !action.CheckLogin() {
					seelog.Errorf("登陆状态为未登陆")
				} else {
					seelog.Info("登陆状态为登陆中")
				}
			case <-alTimer.C:
				err := action.GetLoginData()
				if err != nil {
					seelog.Errorf("自动登陆失败：%v", err)
				}
			}
		}
	}()
}
