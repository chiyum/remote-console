# 本檔是 aiuse 中「Codex 內建子命令」與「吃值的根選項」兩個函式的節錄（與 procscan 的 codexSubcommands／codexValueOptions 對照用）。
# aiuse 不在本 repo；Codex 升版而 aiuse 更新這兩個清單時，同步更新本檔與 procscan.go。
# 也可用 DC_AIUSE_SOURCE=<aiuse 檔案路徑> 直接比對實際安裝的那一份。
codex_known_subcommand() {
  case "$1" in
    agents|exec|e|review|login|logout|mcp|mcp-server|plugin|app-server|remote-control|app|completion|\
    update|doctor|sandbox|debug|apply|a|queue|archive|delete|migrate-rollouts|unarchive|cloud|\
    exec-server|features|help)
      return 0 ;;
  esac
  return 1
}
codex_root_option_takes_value() {
  case "$1" in
    -c|--config|--enable|--disable|--remote|--remote-auth-token-env|-m|--model|--local-provider|-p|--profile|-s|--sandbox|-C|--cd|--add-dir|-a|--ask-for-approval)
      return 0
      ;;
  esac
  return 1
}
