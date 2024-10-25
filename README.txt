cobra init --pkg-name poizon.com/cpu-monitor -a lifeng.ye -l mit
go mod init poizon.com/cpu-monitor
go mod tidy && go mod vendor

rm -rf cpu-monitor && wget http://dw-ops.oss-cn-hangzhou.aliyuncs.com/zingjdk-img/tmp/cpu-monitor && chmod +x cpu-monitor && ./cpu-monitor monitor --interval=1s --warmup=10s --sustain=30s --cooldown=10s --max-log-files=3 --max-log-size=1024 --jfr-duration=10s --threshold=10 --window-size=20
rm -rf cpu-monitor && wget http://dw-ops.oss-cn-hangzhou.aliyuncs.com/zingjdk-img/tmp/cpu-monitor && chmod +x cpu-monitor && ./cpu-monitor --test-collect-logs

