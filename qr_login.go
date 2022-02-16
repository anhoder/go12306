package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/cihub/seelog"
	"github.com/tools/12306/conf"
	"github.com/tools/12306/module"
	"github.com/tools/12306/utils"
	"io/fs"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func CreateImage() (*module.QrImage, error) {
	_, err := utils.RequestGetWithoutJson(utils.GetCookieStr(), "https://kyfw.12306.cn/otn/login/init", nil)
	if err != nil {
		seelog.Error(err)
		return nil, err
	}

	fmt.Println(utils.GetCookieStr())

	data := make(url.Values)
	data.Set("appid", "otn")
	qrImage := new(module.QrImage)
	err = utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/passport/web/create-qr64", qrImage, nil)
	if err != nil {
		seelog.Error(err)
		return nil, err
	}
	if qrImage.ResultCode != "0" {
		seelog.Errorf("create-qr64 fail: %+v", qrImage)
		return nil, errors.New("create-qr64 fail")
	}

	image, err := base64.StdEncoding.DecodeString(qrImage.Image)
	if err != nil {
		seelog.Error(err)
		return nil, err
	}

	err = createQrCode(image)
	if err != nil {
		seelog.Error(err)
		return nil, err
	}

	return qrImage, nil
}

func QrLogin(qrImage *module.QrImage) error {

	// 扫描二维码
	var err error
	data := make(url.Values)
	data.Set("appid", "otn")
	data.Set("uuid", qrImage.Uuid)
	data.Set("RAIL_DEVICEID", utils.GetCookieVal("RAIL_DEVICEID"))
	data.Set("RAIL_EXPIRATION", utils.GetCookieVal("RAIL_EXPIRATION"))
	qrRes := new(module.QrRes)
	for i := 0; i < 60; i++ {
		err = utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/passport/web/checkqr", qrRes, nil)
		if err == nil && qrRes.ResultCode == "2" {
			break
		} else {
			seelog.Infof("请在'./conf/'查看二维码并用12306扫描登陆，二维码暂未登陆，继续查看二维码状态 %v", err)
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		seelog.Error(err)
		return err
	}
	utils.AddCookie(map[string]string{"uamtk": qrRes.Uamtk})

	err = GetLoginData()
	if err != nil {
		seelog.Error(err)
		return err
	}

	return nil
}

func GetLoginData() error {

	// 验证信息，获取tk
	data := make(url.Values)
	data.Set("appid", "otn")

	tk := new(module.TkRes)
	err := utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/passport/web/auth/uamtk", tk, nil)
	if err != nil {
		seelog.Error(err)
		return err
	}
	if tk.ResultCode != 0 {
		seelog.Errorf("uamtk fail: %+v", tk)
		return errors.New("uamtk fail")
	}
	utils.AddCookie(map[string]string{"tk": tk.Newapptk})

	// 通过tk校验
	data.Set("tk", utils.GetCookieVal("tk"))
	userRes := new(module.UserRes)
	err = utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/otn/uamauthclient", userRes, nil)
	if err != nil {
		seelog.Error(err)
		return err
	}
	if userRes.ResultCode != 0 {
		seelog.Error(userRes.ResultMessage)
		return errors.New(userRes.ResultMessage)
	}

	// 初始化api
	apiRes := new(module.ApiRes)
	err = utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/otn/index/initMy12306Api", apiRes, nil)
	if err != nil {
		seelog.Error(err)
		return err
	}
	if !apiRes.Status || apiRes.HTTPStatus != 200 {
		seelog.Errorf("initMy12306Api fail: %+v", apiRes)
		return errors.New("initMy12306Api fail")
	}
	seelog.Infof("%s 登陆成功", apiRes.Data["user_name"])

	// 获取查询或者的query url
	confRes := new(module.InitConfRes)
	err = utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/otn/login/conf", confRes, nil)
	if err != nil {
		seelog.Error(err)
		return err
	}
	conf.QueryUrl = confRes.Data.QueryUrl

	// 获取特殊cookie字段
	staticTk := new(module.TkRes)
	err = utils.Request(data.Encode(), utils.GetCookieStr(), "https://kyfw.12306.cn/passport/web/auth/uamtk-static", staticTk, nil)
	if err != nil {
		seelog.Error(err)
		return err
	}

	return nil
}

func LoginOut() error {
	req, err := http.NewRequest("GET", "https://kyfw.12306.cn/otn/login/loginOut", strings.NewReader(""))
	if err != nil {
		log.Panicln(err)
	}
	req.Header.Set("Cookie", utils.GetCookieStr())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	_, err = utils.GetClient().Do(req)
	if err != nil {
		seelog.Error(err)
	}
	return err
}

func createQrCode(captchBody []byte) error {
	_, err := os.Stat("./conf")
	if err != nil {
		seelog.Warn(err)
		err = os.Mkdir("./conf", os.ModePerm)
		if err != nil {
			seelog.Error(err)
			return err
		}
	}

	imgPath := "./conf/qrcode.png"
	err = ioutil.WriteFile(imgPath, captchBody, fs.ModePerm)
	if err != nil {
		seelog.Error(err)
		return err
	}

	return nil
}
