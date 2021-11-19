#!/usr/bin/env bash
#Requisite 1 - Order and Sets XML files in the Bricklink format
#Requisite 2 - Bricklink Parts catalogs
#Requisite 3 - Bricklink Categories catalog
#Portion 1 of script will combine all items from XMLs into a single CSV with itemid,colorid,qtyid CSV format
#Portion 2 of script will reference Bricklink catalogs to produce the final inventory CSV

SCRIPT_ROOT=$(cd "$(dirname "$0")" && pwd)
TEST_XML=`echo ${SCRIPT_ROOT}"/XMLs/Test"`
XML=`echo ${SCRIPT_ROOT}"/XMLs"`
date > duration_slow.txt

#CATALOG="Catalog"
#BLCOLORS="${CATALOG}/BLColors.txt"
#BLCATEGORY="${CATALOG}/BLCategories.txt"
#BLPARTS="${CATALOG}/BLParts.txt"
#BLMINIFIGS="${CATALOG}/BLMinifigures.txt"
#XML="XMLs"
#TEST="XMLs/Test"
#INVENTORY="./Inventory.csv"

echo -e "\nNormalizing the XML files to add a Color field for all non-part pieces ..."

#for file in `ls -ltr ${TEST_XML}/*.xml | awk '{print $9}'`
for file in `find ${XML} -name *.xml | grep -v Test`
    do
        /home/linuxbrew/.linuxbrew/bin/xmlstarlet ed --inplace -s '//INVENTORY/ITEM[not(COLOR)]' -t elem -n 'COLOR' -v 'NA' $file
        clr=( $(grep -i color $file | cut -f2 -d">" | cut -f1 -d"<") )
        qty=( $(grep -i minqty $file | cut -f2 -d">" | cut -f1 -d"<") )
        item=( $(grep -i itemid $file | cut -f2 -d">" | cut -f1 -d"<") )
        count=`grep -i minqty $file | wc -l`
        
        for k in `seq 0 $(($count-1))`
            do
                echo ${item[k]},${clr[k]},${qty[k]} >> temp.csv
            done
    done

echo -e "... Done\n"

echo -e "Aggregating unique item types and colors ..."

cat temp.csv | awk -F, '{array[$1","$2]+=$3} END { for (i in array) {print i"," array[i]}}' > temp2.csv

itemtotal=`wc -l temp2.csv | awk '{print $1}'`
itemid=(`cat temp2.csv | cut -d"," -f1`)
colorid=(`cat temp2.csv | cut -d"," -f2`)
itemqty=(`cat temp2.csv | cut -d"," -f3`)
echo -e "... Done\n"

echo -e "Category;ColorID;Color;ItemID;Item;Quantity" > Inventory.csv

echo -e "Referencing BrickLink Catalog to populate final inventory ...\n"

for i in `seq 0 1 $(( $itemtotal - 1 ))`
    do
        if [[ ! -z `grep ${itemid[$i]} BLCatalog/BLParts.xml` ]]
        then
            echo -e "Fetching details for ${itemid[$i]} from Parts Catalog. Item ${i} in ${itemtotal}"
            linenum=`cut -d $'\t' -f3 BLCatalog/BLParts.txt | grep -xnF ${itemid[$i]} | cut -d":" -f1`
            itemcategoryname=`sed -n "${linenum}p" BLCatalog/BLParts.txt | cut -d $'\t' -f2`
            itemname=`sed -n "${linenum}p" BLCatalog/BLParts.txt | cut -d $'\t' -f4 | sed 's/[[:space:]]*$//g'`
            itemcolorname=`grep -w "<COLOR>${colorid[$i]}</COLOR>" BLCatalog/BLColors.xml -A 1 | tail -1 | cut -d">" -f2 | cut -d"<" -f1`
        elif [[ ! -z `grep ${itemid[$i]} BLCatalog/BLMinifigures.xml` ]]
        then
            echo -e "Fetching details for ${itemid[$i]} from Minfigs Catalog. Item ${i} in ${itemtotal}"
            linenum=`cut -d $'\t' -f3 BLCatalog/BLMinifigures.txt | grep -xnF ${itemid[$i]} | cut -d":" -f1`
            itemcategoryname=`sed -n "${linenum}p" BLCatalog/BLMinifigures.txt | cut -d $'\t' -f2`
            itemname=`sed -n "${linenum}p" BLCatalog/BLMinifigures.txt | cut -d $'\t' -f4 | sed 's/[[:space:]]*$//g'`
            #itemcolorname=`grep -w "<COLOR>${colorid[$i]}</COLOR>" BLCatalog/BLColors.xml -A 1 | tail -1 | cut -d">" -f2 | cut -d"<" -f1`
            itemcolorname="NA"
        elif [[ ! -z `grep ${itemid[$i]} BLCatalog/BLGear.xml` ]]
        then
            echo -e "Fetching details for ${itemid[$i]} from Gears Catalog. Item ${i} in ${itemtotal}"
            linenum=`cut -d $'\t' -f3 BLCatalog/BLGear.txt | grep -xnF ${itemid[$i]} | cut -d":" -f1`
            itemcategoryname=`sed -n "${linenum}p" BLCatalog/BLGear.txt | cut -d $'\t' -f2`
            itemname=`sed -n "${linenum}p" BLCatalog/BLGear.txt | cut -d $'\t' -f4 | sed 's/[[:space:]]*$//g'`
            itemcolorname=`grep -w "<COLOR>${colorid[$i]}</COLOR>" BLCatalog/BLColors.xml -A 1 | tail -1 | cut -d">" -f2 | cut -d"<" -f1`
        else
            echo -e "${itemid[$i]} is not in any catalog" >> gears.txt
        fi

        echo -e $itemcategoryname";"${colorid[$i]}";"$itemcolorname";"${itemid[$i]}";"$itemname";"${itemqty[$i]} >> Inventory.csv
    done

echo -e "... Done\n"
#rm temp*.csv
date >> duration_slow.txt