/*
Docker Push & Pull

执行 docker push 命令流程：
    1. docker 向 registry 服务器注册 repository： PUT /v1/repositories/<username>/<repository> -> PUTRepository()
    2. 参数是 JSON 格式的 <repository> 所有 image 的 id 列表，按照 image 的构建顺序排列。
    3. 根据 <repository> 的 <tags> 进行循环：
       3.1 获取 <image> 的 JSON 文件：GET /v1/images/<image_id>/json -> image.go#GETJSON()
       3.2 如果没有此文件或内容返回 404 。
       3.3 docker push 认为服务器没有 image 对应的文件，向服务器上传 image 相关文件。
           3.3.1 写入 <image> 的 JSON 文件：PUT /v1/images/<image_id>/json -> image.go#PUTJSON()
           3.3.2 写入 <image> 的 layer 文件：PUT /v1/images/<image_id>/layer -> image.go#PUTLayer()
           3.3.3 写入 <image> 的 checksum 信息：PUT /v1/images/<image_id>/checksum -> image.go#PUTChecksum()
       3.4 上传完此 tag 的所有 image 后，向服务器写入 tag 信息：PUT /v1/repositories/(namespace)/(repository)/tags/(tag) -> PUTTag()
    4. 所有 tags 的 image 上传完成后，向服务器发送所有 images 的校验信息，PUT /v1/repositories/(namespace)/(repo_name)/images -> PUTRepositoryImages()

执行 docker pull 命令流程：
    1. docker 访问 registry 服务器 repository 的 images 信息：GET /v1/repositories/<username>/<repository>/images -> GetRepositoryImages()
    2. docker 访问 registry 服务器 repository 的 tags 信息：GET /v1/repositoies/<username>/<repository>/tags -> GetRepositoryTags()
    3. 根据 <repository> 的 <tags> 中 image 信息进行循环：
      3.1 获取 <image> 的 Ancestry 信息：GET /v1/images/<image_id>/ancestry -> GetImageAncestry()
      3.2 获取 <image> 的 JSON 数据: GET /v1/images/<image_id>/json -> GetImageJson()
      3.3 获取 <image> 的 Layer 文件: GET /v1/images/<image_id/layer -> GetImageLayer()

*/
package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/astaxie/beego"

	"github.com/dockercn/docker-bucket/models"
	"github.com/dockercn/docker-bucket/utils"
)

type RepositoryAPIController struct {
	beego.Controller
}

func (r *RepositoryAPIController) URLMapping() {
	r.Mapping("PutTag", r.PutTag)
	r.Mapping("PutRepositoryImages", r.PutRepositoryImages)
	r.Mapping("GetRepositoryImages", r.GetRepositoryImages)
	r.Mapping("GetRepositoryTags", r.GetRepositoryTags)
	r.Mapping("PutRepository", r.PutRepository)
}

func (this *RepositoryAPIController) Prepare() {
	//相应 docker api 命令的 Controller 屏蔽 beego 的 XSRF ，避免错误。
	this.EnableXSRF = false

	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Registry-Version", beego.AppConfig.String("docker::Version"))
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Registry-Config", beego.AppConfig.String("docker::Config"))
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Encrypt", beego.AppConfig.String("docker::Encrypt"))

	if beego.AppConfig.String("docker::Standalone") == "true" {
		//单机运行模式，检查 Basic Auth 的认证。
		if len(this.Ctx.Input.Header("Authorization")) == 0 {
			//没有 Basic Auth 的认证，返回错误信息。
			beego.Error("没有 Authorization 信息的 API 访问")
			this.Ctx.Output.Context.Output.SetStatus(http.StatusUnauthorized)
			this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"服务器只支持 Basic Auth 验证模式，请联系系统管理员\"}"))
			this.StopRun()
		} else {
			//Standalone True 模式，检查是否 Basic
			if strings.Index(this.Ctx.Input.Header("Authorization"), "Basic") == -1 {
				beego.Error("Authorization 中 Auth 的格式错误")
				this.Ctx.Output.Context.Output.SetStatus(http.StatusUnauthorized)
				this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"HTTP Header 的 Authorization 格式错误\"}"))
				this.StopRun()
			}

			//Decode Basic Auth 进行用户的判断
			username, passwd, err := utils.DecodeBasicAuth(this.Ctx.Input.Header("Authorization"))
			if err != nil {
				beego.Error(fmt.Sprintf("[解码 Basic Auth] %s 错误： %s ", this.Ctx.Input.Header("Authorization"), err.Error()))
				this.Ctx.Output.Context.Output.SetStatus(http.StatusUnauthorized)
				this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"解码 HTTP Header 的 Basic Auth 信息错误\"}"))
				this.StopRun()
			}

			user := new(models.User)
			has, err := user.Get(username, passwd, true)
			if err != nil {
				//查询用户数据失败，返回 401 错误
				user.Log(username, fmt.Sprintf("API 用户访问 Repository 查询用户错误：", err.Error()))
				beego.Error(fmt.Sprintf("[API 用户] 查询用户错误： ", err.Error()))
				this.Ctx.Output.Context.Output.SetStatus(http.StatusUnauthorized)
				this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"查询用户错误\"}"))
				this.StopRun()
			}

			if has == true {
				//查询到用户数据，在以下的 Action 处理函数中使用 this.Data["user"]
				//存储 User 的 Key
				this.Data["username"] = username
				this.Data["passwd"] = passwd
			} else {
				//没有查询到用户数据
				user.Log(username, fmt.Sprintf("API 用户登录 没有查询到用户："))
				this.Ctx.Output.Context.Output.SetStatus(http.StatusForbidden)
				this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"没有查询到用户数据\"}"))
				this.StopRun()
			}
		}
	} else {
		beego.Error("非 Standalone 模式登录尝试错误")
		this.Ctx.Output.Context.Output.SetStatus(http.StatusForbidden)
		this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"服务器只支持 Basic Auth 验证模式，请联系系统管理员\"}"))
		this.StopRun()
	}

}

func (this *RepositoryAPIController) PutRepository() {
	organization := ""

	username := this.Data["username"].(string)
	passwd := this.Data["passwd"].(string)
	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	//判断用户的username和namespace是否相同
	if username != namespace {
		//在未实现组织功能的时候，当用户名和 namespace 不相同时就返回错误信息。
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"错误\":\"用户名和仓库的命名空间不同\"}"))
		this.StopRun()
	} else {
		//TODO 如果是 Organization 用户，判断是否有写入 Organization 的 Repository 权限。
	}

	//创建或更新 Repository 数据
	//也可以采用 ioutil.ReadAll(this.Ctx.Request.Body) 的方式读取 body 数据
	//TODO 检查 JSON 字符串是否合法
	//TODO 检查 逻辑是否合法

	//从 API 创建的 Repository 默认是 Public 的。
	repo := new(models.Repository)
	//由于是非关系型数据库，所以新建和修改是同样操作。
	repo.Add(namespace, repository, organization, string(this.Ctx.Input.CopyBody()))
	repo.SetAgent(namespace, repository, organization, this.Ctx.Input.Header("User-Agent"))
	//如果 Request 的 Header 中含有 X-Docker-Token 且为 True，需要在返回值设置 Token 值。
	//否则客户端报错 Index response didn't contain an access token
	if this.Ctx.Input.Header("X-Docker-Token") == "true" {
		//创建 token 并保存
		//需要加密的字符串为 UserName + UserPassword + 时间戳
		token := utils.GeneralToken(username + passwd)
		this.SetSession("token", token)
		//在返回值 Header 里面设置 Token
		this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Token", token)
		this.Ctx.Output.Context.ResponseWriter.Header().Set("WWW-Authenticate", token)
	}

	this.SetSession("namespace", namespace)
	this.SetSession("repository", repository)
	this.SetSession("access", "write")

	//操作正常的输出
	this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")
	this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Endpoints", beego.AppConfig.String("docker::Endpoints"))

	this.Ctx.Output.Context.Output.SetStatus(http.StatusOK)
	this.Ctx.Output.Context.Output.Body([]byte("\"\""))
}

//TODO：删除一个 Tag 的完整性检查
func (this *RepositoryAPIController) PutTag() {

	beego.Debug("Namespace: " + this.Ctx.Input.Param(":namespace"))
	beego.Debug("Repository: " + this.Ctx.Input.Param(":repo_name"))
	beego.Debug("Tag: " + this.Ctx.Input.Param(":tag"))

	namespace := this.Ctx.Input.Param(":namespace")
	repository := this.Ctx.Input.Param(":repo_name")

	repo := new(models.Repository)
	has, err := repo.Get(namespace, repository, "User")
	if err != nil {
		beego.Error("[Search Repository] " + namespace + " " + repository + " search repository error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	tag := new(models.Tag)
	has, err = tag.Get(namespace, repository, "User", this.Ctx.Input.Param(":tag"))
	if err != nil {
		beego.Error("[Search Tag] " + namespace + " " + repository + " " + this.Ctx.Input.Param(":tag") + " error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search tag encounter error.\"}"))
		this.StopRun()
	}

	//从 HTTP Body 中获取 Image 的 Value
	r, _ := regexp.Compile(`"([[:alnum:]]+)"`)
	imageIds := r.FindStringSubmatch(string(this.Ctx.Input.CopyBody()))

	if has == true {
		//_, err := tag.UpdateImageId(imageIds[1])
		_, err := tag.UpdateImageId(namespace, repository, "User", this.Ctx.Input.Param(":tag"), imageIds[1])

		if err != nil {
			beego.Error("[Update Tag] " + namespace + " " + repository + " " + this.Ctx.Input.Param(":tag") + " error: " + err.Error())
			this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the tag data error.\"}"))
			this.StopRun()
		}
	} else {
		//_, err := tag.Insert(this.Ctx.Input.Param(":tag"), imageIds[1], repo.Id)
		_, err := tag.Insert(namespace, repository, "User", this.Ctx.Input.Param(":tag"), imageIds[1])
		if err != nil {
			beego.Error("[Insert Tag] " + namespace + " " + repository + " " + this.Ctx.Input.Param(":tag") + " error: " + err.Error())
			this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Create the tag record error.\"}"))
			this.StopRun()
		}
	}

	//操作正常的输出
	this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")

	this.Ctx.Output.Context.Output.SetStatus(http.StatusOK)
	this.Ctx.Output.Context.Output.Body([]byte("\"\""))
}

//Push 命令的最后一步，所有的检查操作，通知操作都在此函数进行。
func (this *RepositoryAPIController) PutRepositoryImages() {

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Debug("[Namespace] " + namespace)
	beego.Debug("[Repository] " + repository)

	repo := new(models.Repository)
	has, err := repo.Get(namespace, repository, "User")
	if err != nil {
		beego.Error("[Search Repository] " + namespace + " " + repository + " search repository error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	//计算 repository 的存储量
	var size int64

	if has == false {
		//在上传的最后如果从服务器无法查询到 repository 数据，返回 404 报错。
		beego.Error("[Search Repository] " + namespace + " " + repository + " search repository has none.")
		this.Ctx.Output.Context.Output.SetStatus(http.StatusNotFound)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found repository.\"}"))
		this.StopRun()

	} else {
		//TODO 检查 Image 的 Tag 信息和上传的 Tag 信息是否一致。

		//检查 Repository 的所有 Image Layer 是否都上传完成。
		var images []map[string]string
		uploaded := true
		checksumed := true

		//解析保存的 JSON 字符串信息为一个 image 的数组，image 的格式包含 id 和 Tag 两项。
		//{"id":"ffe35e09aeec0f3f9daf48ea9a949dea2b240137e24a374c47493a754a5b338b","Tag":"latest"}
		json.Unmarshal([]byte(repo.JSON), &images)

		for _, i := range images {
			image := new(models.Image)
			has, err := image.Get(i["id"])
			if err != nil {
				beego.Error("[Search Image] " + i["id"] + " search image error: " + err.Error())
				this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
				this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search image error.\"}"))
				this.StopRun()
			}

			if has == false {
				//搜索不到 Image 数据也同样返回 404 错误。
				beego.Error("[Search Image] " + i["id"] + " search image has none.")
				this.Ctx.Output.Context.Output.SetStatus(http.StatusNotFound)
				this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found image.\"}"))
				this.StopRun()
			} else {
				//如果有一个 Image 的上传完成标志 false ，终止循环返回错误退出。
				if image.Uploaded == false {
					uploaded = false
					break
				}

				//如果有一个 Image 的 Checksumed 标志 false ，终止循环返回错误退出。
				if image.CheckSumed == false {
					checksumed = false
					break
				}

				//计算所有的 Image Size 总和。
				size += image.Size
			}
		}

		//因为不是所有的 Image 都上传成功，所以返回错误信息。
		if uploaded == false {
			beego.Error("[Put Repository] " + namespace + " " + repository + " not all image uploaded.")
			this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"The image layer upload not complete, please try again.\"}"))
			this.StopRun()
		}

		//因为不是所有的 Image 都检查成功，所以返回错误信息。
		if checksumed == false {
			beego.Error("[Put Repository] " + namespace + " " + repository + " not all image checksumed.")
			this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
			this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"The image layer upload checksumed error, please try again.\"}"))
			this.StopRun()
		}
	}

	//_, err = repo.UpdateUploaded(true)
	_, err = repo.UpdateRepositoryInfo(namespace, repository, "User", "Uploaded", strconv.FormatBool(true))
	if err != nil {
		beego.Error("[Update Repository] " + namespace + " " + repository + " update uploaded error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository uploaded flag error, please try again.\"}"))
		this.StopRun()
	}

	//检查 docker 命令发上来的 Checksum 值。

	//_, err = repo.UpdateChecksumed(true)
	_, err = repo.UpdateRepositoryInfo(namespace, repository, "User", "Checksumed", strconv.FormatBool(true))
	if err != nil {
		beego.Error("[Update Repository] " + namespace + " " + repository + " update checksumed error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository checksumed flag error, please try again.\"}"))
		this.StopRun()
	}

	//_, err = repo.UpdateSize(size)
	_, err = repo.UpdateRepositoryInfo(namespace, repository, "User", "Checksumed", strconv.FormatInt(size, 10))

	if err != nil {
		beego.Error("[Update Repository] " + namespace + " " + repository + " update size error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Update the repository size error, please try again.\"}"))
		this.StopRun()
	}

	//操作正常的输出
	this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")

	this.Ctx.Output.Context.Output.SetStatus(http.StatusOK)
	this.Ctx.Output.Context.Output.Body([]byte("\"\""))
}

//获取一个 Repository 的 Image 信息
func (this *RepositoryAPIController) GetRepositoryImages() {

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Debug("[Namespace] " + namespace)
	beego.Debug("[Repository] " + repository)

	//查询 Repository 数据
	repo := new(models.Repository)

	//查询已经完成上传的 Repository
	//**这句话判断依据忘记了,是所有Image都完毕？--fivestarsky
	has, err := repo.GetActived(namespace, repository)

	if err != nil {
		beego.Error("[Search Repository] " + namespace + " " + repository + " search pushed error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	if has == false {
		beego.Error("[Search Repository] " + namespace + " " + repository + " search pushed has none.")
		this.Ctx.Output.Context.Output.SetStatus(http.StatusNotFound)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found repository.\"}"))
		this.StopRun()
	} else {

		if repo.Privated == true {
			//TODO 私有库要判断当前用户是不是同 namespace 相同。
			user := this.Data["user"].(*models.User)
			if user.Username != namespace {
				beego.Error("[Get Repository] " + namespace + " " + repository + " private repository download from " + user.Username)
				this.Ctx.Output.Context.Output.SetStatus(http.StatusUnauthorized)
				this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Download private repository error.\"}"))
				this.StopRun()
			}
			//TODO 判断当前用户是不是属于 Organization
		}

		//设定 Session 的权限
		this.SetSession("access", "read")

		//操作正常的输出
		this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")
		this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Endpoints", beego.AppConfig.String("docker::Endpoints"))

		//公有库直接返回保存的 JSON 信息。
		this.Ctx.Output.Context.Output.SetStatus(http.StatusOK)
		this.Ctx.Output.Context.Output.Body([]byte(repo.JSON))
	}
}

func (this *RepositoryAPIController) GetRepositoryTags() {

	//获取namespace/repository
	namespace := string(this.Ctx.Input.Param(":namespace"))
	repository := string(this.Ctx.Input.Param(":repo_name"))

	beego.Debug("[Namespace] " + namespace)
	beego.Debug("[Repository] " + repository)

	//查询 Repository 数据
	repo := new(models.Repository)
	//查询已经完成上传的 Repository
	//**这句话判断依据忘记了,是所有Image都完毕？--fivestarsky
	has, err := repo.GetActived(namespace, repository)

	if err != nil {
		beego.Error("[Search Repository] " + namespace + " " + repository + " search pushed error: " + err.Error())
		this.Ctx.Output.Context.Output.SetStatus(http.StatusBadRequest)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Search repository error.\"}"))
		this.StopRun()
	}

	if has == false {
		beego.Error("[Search Repository] " + namespace + " " + repository + " search pushed has none.")
		this.Ctx.Output.Context.Output.SetStatus(http.StatusNotFound)
		this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found repository.\"}"))
		this.StopRun()
	} else {

		if repo.Privated == true {
			//TODO 私有库要判断当前用户是不是同 namespace 相同。
			//TODO 判断当前用户是不是属于 Organization
		}

		//存在 Repository 数据，查询所有的 Tag 数据。
		//tag := new(models.Tag)

		//**这块应该设计从哪里取得？--fviestarksy
		//result, err := tag.GetImagesJSON(repo.Id)
		//if err != nil {
		//	beego.Error("[Search Tags] " + namespace + " " + repository + " search pushed tags error: " + err.Error())
		//	this.Ctx.Output.Context.Output.SetStatus(http.StatusNotFound)
		//	this.Ctx.Output.Context.Output.Body([]byte("{\"error\":\"Cloud not found tags.\"}"))
		//	this.StopRun()
		//}

		//操作正常的输出
		this.Ctx.Output.Context.ResponseWriter.Header().Set("Content-Type", "application/json;charset=UTF-8")
		this.Ctx.Output.Context.ResponseWriter.Header().Set("X-Docker-Endpoints", beego.AppConfig.String("docker::Endpoints"))

		this.Ctx.Output.Context.Output.SetStatus(http.StatusOK)
		//this.Ctx.Output.Context.Output.Body(result)
		this.Ctx.Output.Context.Output.Body([]byte(""))
	}
}
