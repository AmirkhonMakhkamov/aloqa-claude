import { Smartphone } from "lucide-react";
import { ViewPlaceholder } from "@/components/shell/ViewPlaceholder";

/*
 * Telephony view. The backend has no PSTN / SIP surface today, so this
 * view is an explicit "not wired" state rather than stubbing a dialpad
 * that calls nothing.
 */
export default function TelephonyPage() {
  return (
    <ViewPlaceholder
      Icon={Smartphone}
      title="Phone"
      body="PSTN dial-in isn't connected. Use Meetings for peer-to-peer calls in the meantime."
    />
  );
}
