FROM registry.ngaiot.com/tools/golang:1.23

WORKDIR /app/
COPY . .
RUN unset GOPROXY
RUN go env -w GOPROXY=https://goproxy.cn
RUN go env -w GO111MODULE=on
RUN cd video-recognition && go mod tidy && CGO_ENABLED=0 go build -ldflags "-extldflags -static " -tags netgo -a -v -o video-recognition main.go


FROM repository.anxinyun.cn/devops/alpine:3-tz-hw
#RUN apk update && apk add tzdata
WORKDIR /usr/local/bin
COPY  --from=0 /app/video-recognition/conf.ini /usr/local/bin
COPY  --from=0 /app/video-recognition/*.html /usr/local/bin
COPY  --from=0 /app/video-recognition/video-recognition /usr/local/bin

RUN chmod +x /usr/local/bin/video-recognition

CMD ["video-recognition"]