// Settings › General › Agent (#2475): the hub-wide switches about agents. "Workflow board" is off unless turned on here; the switch
// is the hub's, so every browser shows the same (lib/workspace-board-setting.ts).
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { setWorkspaceBoardEnabled, useWorkspaceBoardSetting } from "../../lib/workspace-board-setting";
import { Spinner, toast } from "../../ui/atoms";
import { Group, Row, Switch } from "./SettingsParts";

export function AgentGroup() {
  const board = useWorkspaceBoardSetting();
  const toggle = (on: boolean) => void setWorkspaceBoardEnabled(on).catch((e) => toast(errorText(e), "error"));
  const readonly = board.enabled !== null && !board.canWrite;
  return (
    <Group title={t("workspace-board.setting_group")} anchor="agent-group">
      <Row label={t("workspace-board.setting_label")} hint={readonly ? t("workspace-board.setting_readonly") : t("workspace-board.setting_hint")}>
        {board.enabled === null ? <Spinner /> : <Switch on={board.enabled} onChange={toggle} disabled={readonly} label={t("workspace-board.setting_label")} />}
      </Row>
    </Group>
  );
}
