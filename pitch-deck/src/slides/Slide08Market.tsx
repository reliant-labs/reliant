import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 08: Market. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "market")!;
export default function Slide08Market() {
  return <SlideLayout data={data} />;
}
