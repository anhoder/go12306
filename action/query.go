package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/yincongcyincong/go12306/module"
	"github.com/yincongcyincong/go12306/utils"
)

var (
	TokenRe           = regexp.MustCompile("var globalRepeatSubmitToken = '(.+)';")
	TicketInfoRe      = regexp.MustCompile("var ticketInfoForPassengerForm=(.+);")
	OrderRequestParam = regexp.MustCompile("var orderRequestDTO=(.+);")
)

func GetTrainInfo(searchParam *module.SearchParam) ([]*module.TrainData, error) {

	var err error
	searchRes := new(module.TrainRes)
	cdn := utils.GetCdn()
	if utils.InBlackList(cdn) {
		return nil, errors.New(fmt.Sprintf("%s 在小黑屋", cdn))
	}

	err = utils.RequestGetWithCDN(utils.GetCookieStr(), fmt.Sprintf("https://kyfw.12306.cn/otn/%s?leftTicketDTO.train_date=%s&leftTicketDTO.from_station=%s&leftTicketDTO.to_station=%s&purpose_codes=ADULT",
		utils.QueryUrl, searchParam.TrainDate, searchParam.FromStation, searchParam.ToStation), searchRes, nil, cdn)
	if err != nil {
		utils.AddBlackList(cdn)
		return nil, err
	}

	if searchRes.HTTPStatus != 200 && searchRes.Status {
		return nil, errors.New(fmt.Sprintf("获取列车信息失败: %+v", searchRes))
	}

	searchDatas := make([]*module.TrainData, len(searchRes.Data.Result))
	for i, res := range searchRes.Data.Result {
		resSlice := strings.Split(res, "|")
		sd := new(module.TrainData)
		sd.Status = resSlice[1]
		sd.TrainName = resSlice[2]
		sd.TrainNo = resSlice[3]
		sd.FromStationName = searchRes.Data.Map[resSlice[6]]
		sd.ToStationName = searchRes.Data.Map[resSlice[7]]
		sd.FromStation = resSlice[6]
		sd.ToStation = resSlice[7]

		if resSlice[1] == "预订" {
			sd.SecretStr = resSlice[0]
			sd.LeftTicket = resSlice[29]
			sd.StartTime = resSlice[8]
			sd.ArrivalTime = resSlice[9]
			sd.DistanceTime = resSlice[10]
			sd.IsCanNate = resSlice[37]

			sd.SeatInfo = make(map[string]string)
			sd.SeatInfo["特等座"] = resSlice[utils.SeatType["特等座"]]
			sd.SeatInfo["商务座"] = resSlice[utils.SeatType["商务座"]]
			sd.SeatInfo["一等座"] = resSlice[utils.SeatType["一等座"]]
			sd.SeatInfo["二等座"] = resSlice[utils.SeatType["二等座"]]
			sd.SeatInfo["软卧"] = resSlice[utils.SeatType["软卧"]]
			sd.SeatInfo["硬卧"] = resSlice[utils.SeatType["硬卧"]]
			sd.SeatInfo["硬座"] = resSlice[utils.SeatType["硬座"]]
			sd.SeatInfo["无座"] = resSlice[utils.SeatType["无座"]]
			sd.SeatInfo["动卧"] = resSlice[utils.SeatType["动卧"]]
			sd.SeatInfo["软座"] = resSlice[utils.SeatType["软座"]]
		}

		searchDatas[i] = sd
	}
	return searchDatas, nil
}

func GetRepeatSubmitToken() (*module.SubmitToken, error) {

	body, err := utils.RequestGetWithoutJson(utils.GetCookieStr(), "https://kyfw.12306.cn/otn/confirmPassenger/initDc", nil)

	matchRes := TokenRe.FindStringSubmatch(string(body))
	submitToken := new(module.SubmitToken)
	if len(matchRes) > 1 {
		submitToken.Token = matchRes[1]
	}

	ticketRes := TicketInfoRe.FindSubmatch(body)
	if len(ticketRes) > 1 {
		ticketRes[1] = bytes.Replace(ticketRes[1], []byte("'"), []byte(`"`), -1)
		err = json.Unmarshal(ticketRes[1], &submitToken.TicketInfo)
		if err != nil {
			return nil, err
		}
	}

	orderRes := OrderRequestParam.FindSubmatch(body)
	if len(orderRes) > 1 {
		orderRes[1] = bytes.Replace(orderRes[1], []byte("'"), []byte(`"`), -1)
		err = json.Unmarshal(orderRes[1], &submitToken.OrderRequestParam)
		if err != nil {
			return nil, err
		}
	}

	return submitToken, nil
}

func GetPassengers(submitToken *module.SubmitToken) (*module.PassengerRes, error) {

	data := make(url.Values)
	data.Set("_json_att", "")
	data.Set("REPEAT_SUBMIT_TOKEN", submitToken.Token)
	res := new(module.PassengerRes)
	err := utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/otn/confirmPassenger/getPassengerDTOs", res, nil)
	if err != nil {
		return nil, err
	}

	if res.Status && res.HTTPStatus != 200 {
		return nil, errors.New(fmt.Sprintf("获取乘客信息失败: %+v", res))
	}

	extraPassenger := make([]*module.Passenger, 0)
	for _, p := range res.Data.NormalPassengers {
		passengerTicketStr := fmt.Sprintf("0,%s,%s,%s,%s,%s,N,%s",
			p.PassengerType, p.PassengerName, p.PassengerIdTypeCode, p.PassengerIdNo, p.MobileNo, p.AllEncStr)
		oldPassengerStr := fmt.Sprintf("%s,%s,%s,%s_",
			p.PassengerName, p.PassengerIdTypeCode, p.PassengerIdNo, p.PassengerType)
		passengerInfo := fmt.Sprintf("%s#%s#1#%s#%s;", p.PassengerType, p.PassengerName, p.PassengerIdNo, p.AllEncStr)
		p.PassengerTicketStr = passengerTicketStr
		p.OldPassengerStr = oldPassengerStr
		p.PassengerInfo = passengerInfo
		p.Alias = p.PassengerName

		// 乘客类型：1 - 成人票，2 - 儿童票，3 - 学生票，4 - 残军票
		if p.PassengerType != "1" {
			tmpPassenger := *p
			tmpPassenger.PassengerTicketStr = fmt.Sprintf("0,%s,%s,%s,%s,%s,N,%s",
				"1", p.PassengerName, p.PassengerIdTypeCode, p.PassengerIdNo, p.MobileNo, p.AllEncStr)
			tmpPassenger.OldPassengerStr = fmt.Sprintf("%s,%s,%s,%s_",
				p.PassengerName, p.PassengerIdTypeCode, p.PassengerIdNo, "1")
			tmpPassenger.PassengerInfo = fmt.Sprintf("%s#%s#1#%s#%s;", "1", p.PassengerName, p.PassengerIdNo, p.AllEncStr)

			// 把不是特殊票的名称改成特殊票
			switch p.PassengerType {
			case "2":
				p.Alias = p.PassengerName + "-儿童"
			case "3":
				p.Alias = p.PassengerName + "-学生"
			case "4":
				p.Alias = p.PassengerName + "-残军"
			default:
				continue
			}
			extraPassenger = append(extraPassenger, &tmpPassenger)
		}

	}
	res.Data.NormalPassengers = append(res.Data.NormalPassengers, extraPassenger...)

	return res, nil

}

func CheckUser() error {
	data := make(url.Values)
	data.Set("_json_att", "")
	res := new(module.CheckUserRes)
	err := utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/otn/login/checkUser", res, nil)
	if err != nil {
		return err
	}

	if res.Status && res.HTTPStatus != 200 {
		return errors.New(fmt.Sprintf("检查用户失败:%+v", res))
	}

	if !res.Data.Flag {
		return errors.New(fmt.Sprintf("检查用户失败:%+v", res))
	}
	return nil

}
