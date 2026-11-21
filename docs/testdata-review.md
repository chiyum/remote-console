# testdata 逐份檢查紀錄

範圍：`internal/hookstore/testdata/` 全部檔案（共 53 個），以及 `internal/hookstore/hookstore_test.go` 內的字面值。

這些檔案是燈號判讀（`hookstore.ReadScreen`）的測試資料，全部擷取自真實的 Claude Code 終端機畫面（`tmux capture-pane`），擷取時在暫存目錄下以測試用提示詞操作；公開前逐份人工閱讀，個資一律以「同寬度」字串替換，讓畫面欄位對齊、換行位置與判讀結果都不變（替換後 `go test ./internal/hookstore/` 全數通過）。

保留且判定不具識別性的內容：`/private/tmp/claude-501/`（macOS 第一個使用者的預設 uid）、`/home/devuser`、`~/.ai-profiles/claude/abc/`（假 profile 名）、`claude-opus-5-5`（公開的模型名稱）、`a8r4-work`／`S3allow1` 等測試代號、範例散文與「台灣各縣市小吃」之類的測試提示詞、提及 architect 的計畫文字（一般角色名稱）。

sha256 為本次去識別後的檔案內容。

| 檔名 | sha256 | 是否源自真實畫面 | 發現的個資類型 | 處理方式 |
|---|---|---|---|---|
| `screen-after-esc.txt` | `73f4f4c6f8777d9fad20c830c30923f64b486553d66bbc9081f925e68dadacb4` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-allowed-long-running.txt` | `5ceec34b66b389933a344bfd9381dbeb3489693eb666722cb2513cdbb1e76e6a` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-askuserquestion-after-esc.txt` | `6ebe69e937b38226306825ec1ccf39b144846e9c54062ead60ec07030279a696` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-askuserquestion.txt` | `9a4a233fcbce1e231532bf36ff79570b6bd9a3291c2e3e4e892ad444397ac76b` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-bash-after-esc.txt` | `7b153ebe27fb84fb0d68e7996dcfb3c0d6499c1ca1c0cbb57c213175e458b528` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬） |
| `screen-bash-prompt.txt` | `bc492ab39c247853717c503d0cc26cd5ef7fb538cfeda862d8e4c14e6dec59f1` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-ctrl-o-transcript-dialog-words.txt` | `ca0d59625df264432a018c456668dbdd1690274dbb832ccc8cf14c0d7a1b5890` | 是 | 真實 Claude session id（含換行截斷片段） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變） |
| `screen-done.txt` | `7e2ebf4b0a3569563c7c832956aac1e89fcbc8dca91ceea5e571600bed4ad9b6` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-early-esc-long-reply.txt` | `ce3bfe7148896c6c6e58495cc75177d6684cb7ca34a724dd22966be2836a964c` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-edit-prompt.txt` | `32cd8693a028a9fff7adfc893056d12985573a9094eb4dbc077321521fd3e53d` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-exitplanmode-after-esc.txt` | `c7a16666f7a93c329e8efbee657fa5c0b7fd5805b55e49001b7c240a7411f914` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-exitplanmode.txt` | `07e4b74b3133f6ef260f384d4a6e0af6843fb9aacec79ed5e1ffdbf38d792a6a` | 是 | 真實 Claude session id（含換行截斷片段） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變） |
| `screen-long-running-tip-interrupted.txt` | `b6f2d482397d294024069384889919f783783a32ffae380e18f6e143fdf87fbc` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-long-running-tip.txt` | `e9010457a0a8cce739c5b0e63e2a79c59e03bd625f072fe4c808c00b3f471652` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-mcp-prompt.txt` | `3ffa627b637a9ceff05972affb65a7d5081cc1bf7cd1a1cd868a6551d82f8a77` | 是 | 真實 Claude session id（含換行截斷片段） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變） |
| `screen-narrow40-acceptedits-interrupted.txt` | `c101ff87b8593ebfab264e09671a0efe674421d323b40a7bf09268a382d2acc8` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-acceptedits-running.txt` | `749c9d3363600f30078fb7bfa85dd4523ac1f7a661b42b302031ad4a5ce960f7` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-auto-early-esc-interrupted.txt` | `e90682ed00dd5a6f04a9b58daa26e2bbc530b68d854a56973ee55d9c596cc8a2` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-auto-early-esc-prompt-restored.txt` | `4ef74466afda547045d9af4e0ca3dff426fa26cf987b6e7f9e54a0fd0720b96a` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-auto-idle-start.txt` | `6982b4e0661d76a5f98d7a02f18c52dca5ab014a68d21378038fb9ff05349690` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-auto-running.txt` | `31179743e301705f669180e4e9b5459432bf7e0ab8812f5e7752d60d5b1c4a1c` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-auto-running2.txt` | `595f410cf91f7985d260724597e95254c7777363f396afb4c1fd3e5394fd8201` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-bash-prompt.txt` | `de0c6ad56b62313c9c57e63d3832ca2af07358910cc02ee8cb98b6119628b0df` | 是 | 真實 Claude session id（含換行截斷片段）；家目錄使用者名稱（暫存路徑 -Users-<名稱>，換行截斷） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成 -Users-tester（同長度切行） |
| `screen-narrow40-ctrl-o-transcript-dialog-words.txt` | `e8e2c779f46c73746d206dc6bbd651dfc8c1818c62b17afdd39ada30fb906242` | 是 | 真實 Claude session id（含換行截斷片段）；家目錄使用者名稱（暫存路徑 -Users-<名稱>，換行截斷） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成 -Users-tester（同長度切行） |
| `screen-narrow40-interrupted.txt` | `8c10e3d6bc061acc22ee00406732da8405368a64ffa59d82ce0a414ad5f4968f` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-narrow40-prompt-with-tasks-after-esc.txt` | `f3b5fbe8dca0939f0f36cb0019bf9b8ae60be7cec2c4fbd1cc9663601f114b35` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-prompt-with-tasks.txt` | `903ffc2838982d0ae7df4f90d6a0b7cc59126929e3e8b75443be8ce6512ccb12` | 是 | 真實 Claude session id（含換行截斷片段）；家目錄使用者名稱（暫存路徑 -Users-<名稱>，換行截斷） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成 -Users-tester（同長度切行） |
| `screen-narrow40-running.txt` | `e6b20562d1c8ba095dcaa3c09aa128fcb2881e7df3a37e5dd2a36207640435fa` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-narrow40-todos-running.txt` | `55d0cbeb4f9533711efc6873ebd9b7842eb42595672610f289351bf7b416ad86` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-narrow40-working-after-ctrl-o.txt` | `b9405b3d50e5947dd0a653d0fe252fabf7c27ae3971331ce2d218498f0699ac0` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-working-at-menu.txt` | `65f41798076ab9566a035477b7d4d23aa8615e2a291ff0319194f4d66be0a0f8` | 是 | 訂閱方案與模型字樣；Claude Code 版本號；個人 agent 選單（@ 補全清單） | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化；個人 agent 名稱與說明改成 agent-one、example-agent-b（同寬）；Claude Code 內建的 claude、claude-code-guide 保留 |
| `screen-narrow40-working-ctrl-o.txt` | `7e25516bf11ec1845ac787852a010114a94c1f41b853f833b88a9842759c3ceb` | 是 | 訂閱方案與模型字樣；Claude Code 版本號 | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-narrow40-working-slash-menu.txt` | `d9db409ac8a09c67929f60141e5988787b7b1d869d9462d7563e4aa5870a3614` | 是 | 訂閱方案與模型字樣；Claude Code 版本號；個人 skill 選單（slash 補全清單） | 改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化；改成通用假指令 /alpha、/beta 與範例說明（同寬，保留選單結構） |
| `screen-rows28-fresh-early-esc-prompt-restored.txt` | `80bf9822f61d2a61d1d3020b1fa9e538542154ccca8524e1b3ab42ac42194b4a` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-rows50-fresh-0.5s-after-send.txt` | `9f864dbf56b3add360c907bb593d2be771aa6aa93915d6774d887cfb5a41f9f2` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-rows65-fresh-0.5s-after-send.txt` | `1a74ce23c6d5ad164599372d350bdd2c7b13ea9ed5e0513d509eae938672c879` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-rows65-fresh-1s-after-send.txt` | `c0e22ed8e44570845704fad183e91af1e74cb8a191677f120226891255385106` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-rows65-fresh-early-esc-prompt-restored.txt` | `0648ef99c6e997ac1aa06e1e4d360e7217f352de085b2608e71ff57d5e34f6d4` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-tall50-fresh-early-esc-prompt-restored.txt` | `0a4ad8cb29f7c05d5fc3af086f4026bbc5b7ba31f9f950d7c91a6e5a962dfd98` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-tall50-fresh-streaming.txt` | `4b5077f919246fdd52e6d5e1f2014e6a794d77becce4b54f570c202a39eb8c56` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-tall50-warm-early-esc-prompt-restored.txt` | `d2dad78f763d7426083be4603591a5d9c509c5bdfdb183b26d43e0cb61b5ad0c` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-text-interrupted.txt` | `51ad4ac6d2dc10a908d28ce0d08b0c90cce63900f2dd865f41020f7592c59eb9` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-todos-interrupted-wide.txt` | `7bfbac3e30b5de72ef26d232a0df55dee051dfe35b62e3b183f597af86062e02` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-todos-running-wide.txt` | `9321f2da99d0d8c2c5f958bd6ff73dc81cdfa23a30270a6d60aedbfe20971f7a` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-tool-interrupted.txt` | `f9ba83e357277646f69b2a5381b748cd65838e55cd4a2de07fef18a7729ddd59` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-webfetch-prompt.txt` | `1e40d4e196f4994b588bb9a1674a3f965a964520c56d02e1cfb373e88eb75e5b` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-working-output-has-dialog-words.txt` | `85dc73e8853f31ea740c7afecf9757bc990031cee4a1a4738e53078921cf5af0` | 是 | 真實 Claude session id（含換行截斷片段） | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變） |
| `screen-working-streaming.txt` | `073c0c834e33edcd98caad67e03d2219230503b6c50eb5eb49722e229032928b` | 是 | 無 | 保留：內容只有測試用提示詞（/tmp 暫存路徑、範例散文、a.txt 等）與 Claude Code 通用介面文字 |
| `screen-write-after-esc.txt` | `af390d7332cee982ff5c6ef0b84b369e4efb85a868910434a3c732c34f989d26` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-write-prompt.txt` | `686f9150fc8c561d208f2a273685ef2aac6f45320bccc5c0285bb108af97f149` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-write-tab-amend-esc1.txt` | `2dcc228c13f65cc6b8d0d4ab176a00714da93bfb886e96daee5f6867654569aa` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-write-tab-amend-typed.txt` | `cc46264c60d684c394c8ceb800622bffab90e657de595b1aebcb61143be434f4` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |
| `screen-write-tab-amend.txt` | `0a96e2cab3e7607e815bab92054b78afdaa3eee166ca82dc5f0324faa5484f15` | 是 | 真實 Claude session id（含換行截斷片段）；訂閱方案與模型字樣；Claude Code 版本號 | 換成假 UUID aaaaaaaa-0000-4000-8000-000000000001（同長度，截斷位置不變）；改成中性字樣 Model 0.0 · Test Plan（同寬）；改成假版本 v0.0.000（同寬）；版本號本身不具識別性，為避免透露擷取時間仍中性化 |

## hookstore_test.go 字面值

| 位置 | 原內容類型 | 處理方式 |
|---|---|---|
| `TestNoPaneUsesSessionKey` 的 `sid`（第 64 行） | 真實 Claude session id | 換成假 UUID `aaaaaaaa-0000-4000-8000-000000000001`；其他套件測試中的第二個真實 id 換成 `bbbbbbbb-0000-4000-8000-000000000002` |
| 其餘字面值（事件名稱、`%N` pane id、`/tmp/x.jsonl` 等） | 無 | 保留（皆為測試自造值） |
